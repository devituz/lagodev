package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/migrations"
)

// App — ilovaning tepa darajadagi konteyner. Marshrutlar, ulanish,
// sozlamalar va HTTP server'ni birgalikda saqlaydi. Foydalanuvchi
// faqat main.go'da `web.New(...).Run()` chaqiradi.
type App struct {
	*Router // Top-level marshrutlash uchun embedded

	conn       *database.Connection
	manager    *database.Manager
	server     *http.Server
	migrate    bool
	migrations *migrations.Registry
	addr       string
	timeout    time.Duration
	logger     *log.Logger
	mu         sync.Mutex
}

// Option — funksional sozlash usuli.
type Option func(*App)

// WithDatabase — ochiq *database.Connection'ni ilovaga biriktiradi.
// Handler'lardan `c.DB` orqali foydalanish mumkin.
func WithDatabase(conn *database.Connection) Option {
	return func(a *App) { a.conn = conn }
}

// WithManager — *database.Manager'ni biriktiradi. Run() chiqqach Close()
// chaqiriladi.
func WithManager(mgr *database.Manager) Option {
	return func(a *App) { a.manager = mgr }
}

// WithMigrations — Run() vaqtida migratsiyalarni avtomat qo'llashni yoqadi.
// Registry nil bo'lsa, package-default ishlatiladi.
func WithMigrations(reg *migrations.Registry) Option {
	return func(a *App) {
		a.migrate = true
		a.migrations = reg
	}
}

// WithAddr — server tinglovchi manzili (default ":8080").
func WithAddr(addr string) Option {
	return func(a *App) { a.addr = addr }
}

// WithShutdownTimeout — graceful shutdown vaqti (default 10s).
func WithShutdownTimeout(d time.Duration) Option {
	return func(a *App) { a.timeout = d }
}

// New — yangi App yaratadi va kerakli o'rta dasturlarni standart qilib
// qo'shadi (Logger + Recovery). Disable qilish uchun Reset() chaqiring.
func New(opts ...Option) *App {
	a := &App{
		Router:  newRouter(),
		addr:    ":8080",
		timeout: 10 * time.Second,
		logger:  log.New(os.Stdout, "", log.LstdFlags),
	}
	for _, o := range opts {
		o(a)
	}
	// Standart middleware'lar
	a.Use(Logger(a.logger), Recovery(a.logger))
	return a
}

// DB — biriktirilgan database.Connection'ni qaytaradi (yo'q bo'lsa nil).
func (a *App) DB() *database.Connection { return a.conn }

// Run — HTTP serverni ishga tushiradi va Ctrl+C kelguncha bloklanadi.
// Graceful shutdown signali kelganda joriy so'rovlar tamomlanguncha kutadi.
func (a *App) Run(addr ...string) error {
	if len(addr) > 0 && addr[0] != "" {
		a.addr = addr[0]
	}

	// 1. Agar WithMigrations(...) berilgan bo'lsa, avtomat qo'llaymiz
	if a.migrate && a.conn != nil {
		if _, err := migrations.New(a.conn, a.migrations, migrations.Options{}).Up(context.Background()); err != nil {
			return fmt.Errorf("web: migrate: %w", err)
		}
	}

	// 2. Marshrutlarni ServeMux'ga yozamiz. Handler (any, error) qaytaradi —
	//    ctx.respond ularni HTTP javobiga aylantiradi (Laravel uslubi).
	mux := http.NewServeMux()
	for _, rt := range a.Routes() {
		method, path, h := rt.Method, rt.Path, rt.Handler
		mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			ctx := newContext(w, r, a.conn)
			value, err := h(ctx)
			ctx.respond(value, err)
		})
	}

	// 3. Server obyekti — slowloris va resurs ushlab qolish hujumlaridan
	//    himoyalanish uchun barcha timeoutlar belgilangan.
	a.server = &http.Server{
		Addr:              a.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}

	// 4. Marshrutlar jadvalini chiqaramiz (debug uchun foydali)
	a.printRoutes()

	// 5. Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		a.logger.Printf("listening on http://localhost%s", a.addr)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case s := <-sig:
		a.logger.Printf("signal received (%s); graceful shutdown…", s)
		ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
		defer cancel()
		if err := a.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("web: shutdown: %w", err)
		}
		if a.manager != nil {
			_ = a.manager.Close()
		}
		return nil
	}
}

// MustRun — Run() qaytargan xato bo'lsa fatal log bilan to'xtaydi.
// Asosiy main() uchun qulay.
func (a *App) MustRun(addr ...string) {
	if err := a.Run(addr...); err != nil {
		a.logger.Fatalf("web: %v", err)
	}
}

func (a *App) printRoutes() {
	routes := a.Routes()
	if len(routes) == 0 {
		return
	}
	a.logger.Println("registered routes:")
	maxMethod := 0
	for _, r := range routes {
		if l := len(r.Method); l > maxMethod {
			maxMethod = l
		}
	}
	for _, r := range routes {
		pad := strings.Repeat(" ", maxMethod-len(r.Method))
		a.logger.Printf("  %s%s  %s", r.Method, pad, r.Path)
	}
}
