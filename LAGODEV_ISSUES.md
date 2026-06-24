# lagodev — uchragan muammolar (community uchun)

`github.com/devituz/lagodev@v0.19.0` bilan ishlashda topilgan xato/nomuvofiqliklar.
Har biri qisqacha tavsif + reproduce + vaqtinchalik yechim (workaround) bilan.

> **STATUS (4/4 RESOLVED):** 1, 2, 3, 4 — barchasi tuzatildi. Quyida har biriga
> "✅ Fix" izohi qo'shilgan. Regression testlar: `schema/schema_test.go`,
> `schema/realdb_integration_test.go` (real SQLite), `auth/auth_test.go`,
> `orm/e2e_realproject_test.go` (CRUD + JWT + RBAC end-to-end).

---

## 1. `t.ID()` Postgres'da PRIMARY KEY yaratmaydi

**Tavsif:** `schema.Blueprint.ID()` ustunga `IsPrimary: true` qo'yadi, lekin
Postgres grammatikasida single-column PK na inline (`PRIMARY KEY`), na table-level
constraint sifatida render bo'ladi — faqat `BIGSERIAL` chiqadi. Natijada jadvalda
formal primary key bo'lmaydi va unga FK qo'yib bo'lmaydi:
`ERROR: there is no unique constraint matching given keys for referenced table` (SQLSTATE 42830).

**Reproduce:**
```go
schema.Create("regions", func(t *schema.Blueprint) { t.ID(); t.JSONB("name") })
schema.Create("districts", func(t *schema.Blueprint) {
    t.ID()
    t.UnsignedBigInteger("region_id")
    t.Foreign("region_id").References("id").On("regions") // ← 42830
})
```

**Sabab (grammar.go):** `compileCreate` faqat `len(pkCols) > 1` bo'lganda table-level
PK qo'shadi; `compileColumnInline` esa `IsPrimary && Name != "postgres"` bo'lgandagina
inline PK yozadi. Demak Postgres'da single-column `t.ID()` PK'siz qoladi.

**Workaround:** `t.ID()` yoniga `t.Primary("id")` qo'shish PK beradi — LEKIN bu
SQLite'da ikki marta PK bo'lib `table has more than one primary key` xatosini beradi
(2-muammoga qarang). Hozircha loyihada FK'lar defer qilingan.

**✅ Fix (`schema/grammar.go`):** PK rendering markazlashtirildi —
`primaryKeyColumns()` `IsPrimary` ustunlar bilan `Primary()` indexni birlashtirib
dedupe qiladi. Single-column PK barcha dialektlarda **inline** (`BIGSERIAL NOT NULL
PRIMARY KEY` Postgres'da), composite PK esa table-level. Endi `t.ID()` Postgres'da
ham haqiqiy PK beradi va FK ishlaydi.

---

## 2. `t.ID()` + `t.Primary("id")` SQLite'da ikki marta PK

**Tavsif:** Postgres uchun PK olish maqsadida `t.Primary("id")` qo'shilsa, SQLite'da
`t.ID()` allaqachon inline `PRIMARY KEY` yozgani uchun:
`migrations: up ...: table "X" has more than one primary key`.

**Natija:** Bir xil migratsiya kodi Postgres (prod) va SQLite (test) da bir vaqtda
ishlamaydi — dialekt nomuvofiqligi. 1 va 2 birga: single-column PK uchun ikkala
dialektda ishlaydigan yagona variant yo'q.

**✅ Fix:** Dedupe tufayli `t.ID()` + `t.Primary("id")` endi PK'ni bir marta
chiqaradi (SQLite'da `more than one primary key` yo'q). Eng yaxshisi: faqat
`t.ID()` yozish kifoya — endi har uch dialektda PK beradi, `t.Primary("id")`
qo'shish shart emas.

---

## 3. `t.Boolean(...).Default(true/false)` Postgres'da integer default beradi

**Tavsif:** Boolean ustunga `.Default(true)` yoki `.Default(false)` berilsa, default
ifoda `1`/`0` (integer) sifatida render bo'ladi va Postgres rad etadi:
`ERROR: column "is_active" is of type boolean but default expression is of type integer` (SQLSTATE 42804).

**Reproduce:**
```go
t.Boolean("is_active").Default(true) // ← 42804 (Postgres)
```

**Workaround:** boolean ustunga default bermaslik (model qiymatni o'zi belgilaydi),
yoki qo'lda `DEFAULT TRUE` raw bilan yozish. `formatDefault` bool uchun `TRUE/FALSE`
chiqarishi kerak.

**✅ Fix (`schema/grammar.go`):** `Compiler.formatDefault(col)` dialekt-aware bo'ldi —
Postgres uchun bool default `TRUE`/`FALSE`, MySQL `TINYINT(1)` va SQLite uchun
`1`/`0`. `t.Boolean("is_active").Default(true)` endi har uch dialektda ishlaydi.

---

## 4. (eslatma, bug emas) JWT'da jti yo'q — deterministik token

**Tavsif:** `auth.Manager.IssueAccess/IssueRefresh` faqat (user, role, iat, exp)
asosida token yasaydi, unique `jti` qo'shmaydi. Bir soniyada bir xil (user, role)
uchun ikkita refresh token IDENTIK bo'ladi → agar token hash'ini UNIQUE ustunga
saqlasangiz `UNIQUE constraint failed` chiqadi.

**Workaround:** refresh token sifatida JWT emas, opaque `crypto/rand` token
ishlatildi. Taklif: `Issue`'ga random `jti` (RegisteredClaims.ID) qo'shish.

**✅ Fix (`auth/auth.go`):** `issue()` endi har bir tokenga 128-bit `crypto/rand`
`jti` (`RegisteredClaims.ID`) qo'shadi. Bir soniyada bir xil `(user, role, type)`
uchun ham tokenlar UNIQUE — token hash'ini UNIQUE ustunga bemalol saqlash mumkin.
JWT'ni to'g'ridan-to'g'ri refresh token sifatida ishlatsa ham collision yo'q.
