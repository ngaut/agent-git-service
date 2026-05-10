package service

import "fmt"

const mediumTextMaxBytes = (1 << 24) - 1

func validateBodyFitsMediumText(body string) error {
	bodyBytes := len(body)
	if bodyBytes <= mediumTextMaxBytes {
		return nil
	}
	return fmt.Errorf("%w: body exceeds MEDIUMTEXT limit (%d bytes > %d)", ErrValidation, bodyBytes, mediumTextMaxBytes)
}
