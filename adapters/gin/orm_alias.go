package lagogin

import "github.com/devituz/lagodev/orm"

// ormBuilder is a type-alias for orm.Builder[T] so the Paginate generic in
// paginate.go reads cleanly. Keeping it in its own file lets future versions
// of lagogin add convenience helpers without crowding paginate.go.
type ormBuilder[T any] = orm.Builder[T]
