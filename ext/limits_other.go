//go:build !unix && !windows

package ext

func Limits() LimitSupport { return LimitSupport{} }
