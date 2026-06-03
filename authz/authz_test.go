package authz

import (
	"context"
	"errors"
	"testing"
)

type User struct {
	ID   uint64
	Role string
}

type Post struct {
	ID       uint64
	AuthorID uint64
	Draft    bool
}

func TestDefineAndAllows_Gate(t *testing.T) {
	g := New[User]()
	Define(g, "manage-billing", func(_ context.Context, u User, _ any) bool {
		return u.Role == "admin"
	})
	ok, err := g.Allows(context.Background(), "manage-billing", User{Role: "admin"}, nil)
	if err != nil || !ok {
		t.Fatalf("admin allowed = (%v, %v)", ok, err)
	}
	ok, _ = g.Allows(context.Background(), "manage-billing", User{Role: "user"}, nil)
	if ok {
		t.Fatal("non-admin must be denied")
	}
}

func TestDefine_TypedResource(t *testing.T) {
	g := New[User]()
	Define(g, "delete-post", func(_ context.Context, u User, p Post) bool {
		return p.AuthorID == u.ID
	})
	mine := Post{AuthorID: 7}
	notMine := Post{AuthorID: 8}
	if ok, _ := g.Allows(context.Background(), "delete-post", User{ID: 7}, mine); !ok {
		t.Fatal("owner must be allowed")
	}
	if ok, _ := g.Allows(context.Background(), "delete-post", User{ID: 7}, notMine); ok {
		t.Fatal("non-owner must be denied")
	}
}

type PostPolicy struct{}

func (PostPolicy) Update(_ context.Context, u User, p Post) bool {
	return p.AuthorID == u.ID
}
func (PostPolicy) Delete(_ context.Context, u User, p Post) (bool, error) {
	if p.Draft {
		return false, errors.New("drafts cannot be deleted")
	}
	return p.AuthorID == u.ID, nil
}

func TestPolicy_RoutesAbilitiesToMethods(t *testing.T) {
	g := New[User]()
	Policy[Post](g, PostPolicy{})

	mine := Post{AuthorID: 7}
	notMine := Post{AuthorID: 8}

	if ok, _ := g.Allows(context.Background(), "update", User{ID: 7}, mine); !ok {
		t.Fatal("owner update must be allowed")
	}
	if ok, _ := g.Allows(context.Background(), "update", User{ID: 7}, notMine); ok {
		t.Fatal("non-owner update must be denied")
	}
}

func TestPolicy_NormalisesAbilityName(t *testing.T) {
	g := New[User]()
	Policy[Post](g, PostPolicy{})
	// "update" → Update, "Update" → Update (case-insensitive).
	// Ability names with resource suffixes ("update-post") should
	// route via Gate.Define, not policy method lookup.
	for _, name := range []string{"update", "Update", "UPDATE"} {
		ok, err := g.Allows(context.Background(), name, User{ID: 7}, Post{AuthorID: 7})
		if err != nil || !ok {
			t.Fatalf("ability %q must match Update method: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestPolicy_BoolErrSignature(t *testing.T) {
	g := New[User]()
	Policy[Post](g, PostPolicy{})
	_, err := g.Allows(context.Background(), "delete", User{ID: 7}, Post{AuthorID: 7, Draft: true})
	if err == nil {
		t.Fatal("draft delete must surface policy error")
	}
}

func TestBefore_ShortCircuitsToAllow(t *testing.T) {
	g := New[User]()
	Define(g, "ban-user", func(_ context.Context, u User, _ any) bool {
		return u.Role == "moderator"
	})
	Before(g, func(_ context.Context, u User, _ string) bool {
		return u.Role == "superadmin"
	})
	if ok, _ := g.Allows(context.Background(), "ban-user", User{Role: "user"}, nil); ok {
		t.Fatal("plain user must be denied")
	}
	if ok, _ := g.Allows(context.Background(), "ban-user", User{Role: "superadmin"}, nil); !ok {
		t.Fatal("superadmin Before must allow")
	}
}

func TestAuthorize_ReturnsErrDenied(t *testing.T) {
	g := New[User]()
	Define(g, "ability", func(_ context.Context, _ User, _ any) bool { return false })
	err := g.Authorize(context.Background(), "ability", User{}, nil)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("want ErrDenied, got %v", err)
	}
}

func TestAllows_UnknownAbilityErrors(t *testing.T) {
	g := New[User]()
	_, err := g.Allows(context.Background(), "no-such-ability", User{}, nil)
	if !errors.Is(err, ErrUnknownAbility) {
		t.Fatalf("want ErrUnknownAbility, got %v", err)
	}
}

func TestDenies_CollapsesErrToTrue(t *testing.T) {
	g := New[User]()
	if !g.Denies(context.Background(), "missing", User{}, nil) {
		t.Fatal("unknown ability must deny")
	}
}

func TestCheck_RenderableReason(t *testing.T) {
	g := New[User]()
	Define(g, "ability", func(_ context.Context, _ User, _ any) bool { return false })
	d := g.Check(context.Background(), "ability", User{}, nil)
	if d.Allowed || d.Reason == "" {
		t.Fatalf("Decision = %+v", d)
	}
}

func TestPolicy_PointerResource(t *testing.T) {
	g := New[User]()
	Policy[Post](g, PostPolicy{})
	p := &Post{AuthorID: 7}
	if ok, err := g.Allows(context.Background(), "update", User{ID: 7}, p); err != nil || !ok {
		t.Fatalf("pointer resource must dereference: ok=%v err=%v", ok, err)
	}
}

func TestRegistration_IsConcurrencySafe(t *testing.T) {
	g := New[User]()
	done := make(chan struct{}, 2)
	go func() {
		for i := 0; i < 50; i++ {
			Define(g, "x", func(_ context.Context, _ User, _ any) bool { return true })
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 50; i++ {
			_, _ = g.Allows(context.Background(), "x", User{}, nil)
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
