package benchmarks

import (
	"encoding/json"
	"testing"

	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/resource"
)

type apiUser struct {
	ID        int
	Name      string
	Email     string
	Password  string // never serialised
	Admin     bool
	AvatarURL string
}

var userResource = resource.Func[apiUser](func(u apiUser) any {
	return resource.Fields{
		"id":     u.ID,
		"name":   u.Name,
		"email":  u.Email,
		"avatar": u.AvatarURL,
	}.When(u.Admin, "is_admin", true)
})

func sampleUsers(n int) []apiUser {
	out := make([]apiUser, n)
	for i := range out {
		out[i] = apiUser{
			ID:        i + 1,
			Name:      "User Name",
			Email:     "user@example.com",
			Password:  "secret",
			Admin:     i%2 == 0,
			AvatarURL: "https://cdn.example.com/avatar.png",
		}
	}
	return out
}

// BenchmarkResource_Item measures transforming a single model into its
// Fields representation.
func BenchmarkResource_Item(b *testing.B) {
	u := sampleUsers(1)[0]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resource.Item(u, userResource)
	}
}

// BenchmarkResource_Collection measures transforming a 50-row page through
// the resource layer, the typical list-endpoint payload.
func BenchmarkResource_Collection(b *testing.B) {
	users := sampleUsers(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resource.Collection(users, userResource)
	}
}

// BenchmarkResource_CollectionJSON measures the full serialize-then-encode
// path: resource transform followed by encoding/json marshalling, which is
// what a real list endpoint pays per response.
func BenchmarkResource_CollectionJSON(b *testing.B) {
	users := sampleUsers(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := resource.Collection(users, userResource)
		if _, err := json.Marshal(out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResource_Paginated measures rendering an orm.Paginator page,
// including derivation of the meta block.
func BenchmarkResource_Paginated(b *testing.B) {
	page := &orm.Paginator[apiUser]{
		Data:     sampleUsers(15),
		Total:    150,
		Page:     2,
		PerPage:  15,
		LastPage: 10,
		HasMore:  true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resource.Paginated(page, userResource)
	}
}
