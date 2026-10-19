// Package affinity provides soft upstream target affinity. Bindings live in a
// bounded in-process cache; when a Persister is attached they are written behind
// the request path and restored after a restart.
package affinity
