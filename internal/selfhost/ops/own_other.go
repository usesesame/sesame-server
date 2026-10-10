//go:build !unix

package ops

func ownLike(string, string) error { return nil }
