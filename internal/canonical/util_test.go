package canonical

import "encoding/json"

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func sum(a, b float64) float64 { return a + b }

const bs = string(rune(92)) // backslash, kept out of literals because of tool escaping
const q = bs + `"`
