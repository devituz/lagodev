# Encryption, hashing & signing

The `crypt` package gives you two primitives, modelled on Laravel's
`Crypt` facade:

- **Encrypt / Decrypt** — authenticated **AES-256-GCM**. Ciphertext is
  base64-encoded and carries a random nonce, so the same plaintext under
  the same key produces different output every time, and any tampering is
  detected on decrypt.
- **Sign / Verify** — keyed **HMAC-SHA256**. Use this when you only need
  integrity/authenticity (signed URLs, password-reset tokens), not
  confidentiality.

Password hashing (bcrypt) is a separate concern and lives in the `auth`
package — see [Hashing passwords](#hashing-passwords) below.

```go
import "github.com/devituz/lagodev/crypt"
```

Every function is a plain package-level function — there is no client to
construct. You pass the 32-byte key in on each call.

## Quick start

Encrypt a string and read it back:

```go
package main

import (
    "fmt"

    "github.com/devituz/lagodev/crypt"
)

func main() {
    key, _ := crypt.GenerateKey() // 32 random bytes

    box, err := crypt.EncryptString(key, "4111 1111 1111 1111")
    if err != nil {
        panic(err)
    }
    fmt.Println(box) // base64, different on every run

    plain, err := crypt.DecryptString(key, box)
    if err != nil {
        panic(err) // ErrCiphertextMalformed on tamper / wrong key
    }
    fmt.Println(plain) // 4111 1111 1111 1111
}
```

`Encrypt` / `Decrypt` work on `[]byte`; `EncryptString` / `DecryptString`
are convenience wrappers for `string` payloads:

```go
box, err := crypt.Encrypt(key, []byte{0x00, 0x01, 0x02})
raw, err := crypt.Decrypt(key, box) // raw is []byte
```

## Key management

Keys must be **exactly 32 bytes** (`crypt.KeySize`). A wrong-size or empty
key yields `crypt.ErrInvalidKey`.

### Generating a key

```go
// raw 32 bytes — for in-process use / tests
key, err := crypt.GenerateKey()

// "base64:..." string — the format written to APP_KEY in .env
s, err := crypt.GenerateKeyString()
// → "base64:Yk9w...=="
```

In practice you generate the key once with the CLI and store it in `.env`:

```bash
lago key:generate              # writes APP_KEY to .env (refuses to overwrite)
lago key:generate --print-only # just print, touch no file
lago key:generate --force      # overwrite an existing APP_KEY
```

`key:generate` writes the value under `APP_KEY` and preserves every other
line and comment in the file. It will not clobber an existing `APP_KEY`
unless you pass `--force`.

### Loading a key at runtime

`DecodeKey` accepts either form: the `"base64:..."` string produced by
`GenerateKeyString` / `key:generate`, or a raw 32-byte string. Empty or
wrong-size input returns `ErrInvalidKey`.

```go
key, err := crypt.DecodeKey(os.Getenv("APP_KEY"))
if err != nil {
    log.Fatalf("APP_KEY invalid: %v", err)
}

box, _ := crypt.EncryptString(key, secret)
```

Decode once at startup and pass the `[]byte` key down to whatever needs
it — don't re-decode on every call.

## Hashing passwords

Password hashing is **not** in `crypt` (it's a one-way KDF, not reversible
encryption). It lives on the `auth.Manager`, which wraps
`golang.org/x/crypto/bcrypt` at the project's configured cost:

```go
import "github.com/devituz/lagodev/auth"

mgr, err := auth.New(auth.Config{ /* secret, TTLs, BcryptCost */ })
if err != nil {
    log.Fatal(err)
}

// on signup / password change
hash, err := mgr.HashPassword("s3cr3t")        // store hash, never the plaintext
// hash → "$2a$..."

// on login
ok := mgr.VerifyPassword(hash, attempted)       // constant-time bcrypt compare
if !ok {
    return ErrBadCredentials
}
```

`HashPassword` salts internally, so two hashes of the same password differ.
Leave `BcryptCost` at its default unless you have benchmarked otherwise — it
defaults to `bcrypt.DefaultCost` when unset. There is no Argon2 variant in
the framework today.

> Rule of thumb: **encrypt** data you must read back (tokens, PII at rest),
> **hash** data you only ever compare (passwords).

## HMAC signing

When you only need to prove a payload hasn't been altered — and the payload
itself is not secret — sign it instead of encrypting it. `Sign` returns an
unpadded base64url string (`base64.RawURLEncoding`); `Verify` compares in
constant time and returns `ErrSignatureMismatch` on any difference.

```go
key, _ := crypt.GenerateKey()

payload := []byte("uid=42&exp=1750000000")
sig := crypt.Sign(key, payload)

// later, on the inbound request
if err := crypt.Verify(key, payload, sig); err != nil {
    // ErrSignatureMismatch — reject the request
    return err
}
```

### Signed URLs / reset tokens

```go
func signedResetURL(key []byte, base string, uid int, exp time.Time) string {
    q := fmt.Sprintf("uid=%d&exp=%d", uid, exp.Unix())
    sig := crypt.Sign(key, []byte(q))
    return fmt.Sprintf("%s?%s&sig=%s", base, q, sig)
}

func verifyReset(key []byte, q, sig string) error {
    return crypt.Verify(key, []byte(q), sig)
}
```

Because `Sign` is keyed HMAC, an attacker who can't see the key cannot forge
a valid signature, even though the payload travels in plaintext.

## Securing application data

A common pattern is an ORM cast that encrypts a column at rest. The `casts`
package lets you register a named cast and reference it from a struct tag
(see [ORM.md](ORM.md#casts)):

```go
import (
    "github.com/devituz/lagodev/casts"
    "github.com/devituz/lagodev/crypt"
)

type encryptedCast struct{ key []byte }

func (c encryptedCast) ToDB(v any) (any, error) {
    return crypt.EncryptString(c.key, fmt.Sprint(v))
}

func (c encryptedCast) FromDB(v any) (any, error) {
    s, _ := v.(string)
    return crypt.DecryptString(c.key, s)
}

func init() {
    key, _ := crypt.DecodeKey(os.Getenv("APP_KEY"))
    casts.Register("encrypted", encryptedCast{key: key})
}
```

```go
type Account struct {
    orm.Model
    SSN string `column:"ssn" orm:"cast:encrypted"` // ciphertext in the DB
}
```

The model code reads and writes `SSN` as a normal string; the value is
encrypted on write and decrypted on read. (Adapt the method names to your
local `casts.Cast` interface.)

## Production notes

- **Never log plaintext or keys.** Don't put secrets in error messages,
  request dumps, or `fmt.Printf` debugging. The `crypt` errors
  (`ErrInvalidKey`, `ErrCiphertextMalformed`, `ErrSignatureMismatch`) are
  deliberately opaque — keep them that way; don't echo the offending input.
- **Keep `APP_KEY` out of git.** It belongs in `.env` (gitignored) or a
  secret manager / Sealed Secrets / SOPS — never committed.
- **GCM authenticates for you.** A failed `Decrypt` (`ErrCiphertextMalformed`)
  means wrong key *or* tampering — treat both as a hard failure, don't retry
  with a fallback key silently.
- **Key rotation.** GCM nonces are random per message, so reuse under one key
  is not a concern at normal volumes — but you should still rotate keys
  periodically. The key is not embedded in the ciphertext, so to rotate:

  1. Generate a new key (`lago key:generate --print-only`), keep the old one
     available.
  2. On read, try the current key; on `ErrCiphertextMalformed`, fall back to
     the previous key, then re-encrypt the value under the current key.
  3. Once every row has been re-encrypted, drop the old key.

  ```go
  func decryptRotating(cur, prev []byte, box string) (string, bool, error) {
      if s, err := crypt.DecryptString(cur, box); err == nil {
          return s, false, nil // false = no re-encrypt needed
      }
      s, err := crypt.DecryptString(prev, box)
      if err != nil {
          return "", false, err
      }
      return s, true, nil // true = re-encrypt under cur and save
  }
  ```

- **Use the right primitive.** Encrypt for confidentiality, `Sign`/`Verify`
  for integrity-only payloads, `auth.Manager.HashPassword` for passwords.
  Don't reach for one where another fits — e.g. never "encrypt" a password.

## API reference

| Symbol | Purpose |
|--------|---------|
| `crypt.KeySize` | Required key length (32). |
| `crypt.GenerateKey() ([]byte, error)` | 32 random bytes. |
| `crypt.GenerateKeyString() (string, error)` | `"base64:..."` key for `.env`. |
| `crypt.DecodeKey(s string) ([]byte, error)` | Parse raw or `base64:` key. |
| `crypt.Encrypt(key, plaintext []byte) (string, error)` | AES-256-GCM seal. |
| `crypt.EncryptString(key []byte, s string) (string, error)` | String wrapper. |
| `crypt.Decrypt(key []byte, encoded string) ([]byte, error)` | AES-256-GCM open. |
| `crypt.DecryptString(key []byte, encoded string) (string, error)` | String wrapper. |
| `crypt.Sign(key, data []byte) string` | HMAC-SHA256, base64url. |
| `crypt.Verify(key, data []byte, signature string) error` | Constant-time check. |
| `crypt.ErrInvalidKey` | Key not 32 bytes. |
| `crypt.ErrCiphertextMalformed` | Decode/decrypt failure or tamper. |
| `crypt.ErrSignatureMismatch` | `Verify` mismatch. |
| `auth.(*Manager).HashPassword(password string) (string, error)` | bcrypt hash. |
| `auth.(*Manager).VerifyPassword(hash, password string) bool` | bcrypt compare. |
