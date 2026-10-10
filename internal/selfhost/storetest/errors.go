package storetest

import "errors"

func isError(err, want error) bool {
	if want == nil {
		return err == nil
	}
	return errors.Is(err, want)
}
