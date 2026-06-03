package process

import "os"

// osEnviron exists so tests can stub the parent environment.
var osEnviron = os.Environ
