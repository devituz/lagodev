package factory

import (
	"github.com/brianvoe/gofakeit/v7"
)

// Faker wraps gofakeit with stable, allocation-conscious helpers. Each
// Factory owns one Faker; in tests, callers may seed the underlying Faker
// for reproducible builds.
type Faker struct {
	g *gofakeit.Faker
}

// NewFaker constructs an un-seeded Faker.
func NewFaker() *Faker {
	return &Faker{g: gofakeit.New(0)}
}

// NewSeeded constructs a deterministic Faker.
func NewSeeded(seed uint64) *Faker {
	return &Faker{g: gofakeit.New(seed)}
}

// Raw exposes the underlying gofakeit.Faker for advanced usage.
func (f *Faker) Raw() *gofakeit.Faker { return f.g }

func (f *Faker) Name() string              { return f.g.Name() }
func (f *Faker) FirstName() string         { return f.g.FirstName() }
func (f *Faker) LastName() string          { return f.g.LastName() }
func (f *Faker) Email() string             { return f.g.Email() }
func (f *Faker) UserName() string          { return f.g.Username() }
func (f *Faker) Phone() string             { return f.g.Phone() }
func (f *Faker) URL() string               { return f.g.URL() }
func (f *Faker) UUID() string              { return f.g.UUID() }
func (f *Faker) Word() string              { return f.g.Word() }
func (f *Faker) Sentence(words int) string { return f.g.Sentence(words) }
func (f *Faker) Paragraph(paragraphs, sentences, words int, separator string) string {
	return f.g.Paragraph(paragraphs, sentences, words, separator)
}
func (f *Faker) Address() string { return f.g.Address().Address }
func (f *Faker) City() string    { return f.g.City() }
func (f *Faker) Country() string { return f.g.Country() }
func (f *Faker) Password() string {
	return f.g.Password(true, true, true, true, false, 16)
}
func (f *Faker) Bool() bool                            { return f.g.Bool() }
func (f *Faker) IntRange(min, max int) int             { return f.g.IntRange(min, max) }
func (f *Faker) Float64Range(min, max float64) float64 { return f.g.Float64Range(min, max) }
func (f *Faker) PickString(values []string) string     { return f.g.RandomString(values) }
