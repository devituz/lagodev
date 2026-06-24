package inflect

import "testing"

func TestSnake(t *testing.T) {
	cases := map[string]string{
		"User":          "user",
		"UserID":        "user_id",
		"HTTPRequest":   "http_request",
		"PostCommentID": "post_comment_id",
		"already_snake": "already_snake",
		"":              "",
	}
	for in, want := range cases {
		if got := Snake(in); got != want {
			t.Errorf("Snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPluralize(t *testing.T) {
	cases := map[string]string{
		"user":     "users",
		"category": "categories",
		"box":      "boxes",
		"church":   "churches",
		"leaf":     "leaves",
		"life":     "lives",
		"person":   "people",
		"sheep":    "sheep",
		"":         "",
	}
	for in, want := range cases {
		if got := Pluralize(in); got != want {
			t.Errorf("Pluralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTable(t *testing.T) {
	if got := Table("UserProfile"); got != "user_profiles" {
		t.Errorf("Table(UserProfile) = %q, want user_profiles", got)
	}
	if got := Table("Category"); got != "categories" {
		t.Errorf("Table(Category) = %q, want categories", got)
	}
}

func TestForeignKey(t *testing.T) {
	if got := ForeignKey("User"); got != "user_id" {
		t.Errorf("ForeignKey(User) = %q, want user_id", got)
	}
}

func TestPluralize_ZOSisExceptions(t *testing.T) {
	cases := map[string]string{
		"quiz":     "quizzes",
		"hero":     "heroes",
		"potato":   "potatoes",
		"tomato":   "tomatoes",
		"photo":    "photos", // not an exception: plain -s
		"analysis": "analyses",
		"basis":    "bases",
	}
	for in, want := range cases {
		if got := Pluralize(in); got != want {
			t.Errorf("Pluralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSingularize_SesVesExceptions(t *testing.T) {
	cases := map[string]string{
		"buses":      "bus",
		"statuses":   "status",
		"knives":     "knife",
		"lives":      "life",
		"wives":      "wife",
		"wolves":     "wolf",
		"leaves":     "leaf",
		"quizzes":    "quiz",
		"heroes":     "hero",
		"analyses":   "analysis",
		"categories": "category",
	}
	for in, want := range cases {
		if got := Singularize(in); got != want {
			t.Errorf("Singularize(%q) = %q, want %q", in, got, want)
		}
	}
}
