package queue

import "encoding/json"

// jsonImpl exists so queue_test.go can decode payloads without
// littering imports across the file.
func jsonImpl(b []byte, dst any) error { return json.Unmarshal(b, dst) }
