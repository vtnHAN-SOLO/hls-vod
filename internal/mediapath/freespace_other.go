//go:build !linux

package mediapath

import "errors"

// FreeBytes is not implemented off Linux (the platform the media tree
// lives on); callers treat errors.ErrUnsupported as "cannot tell".
func (r *Root) FreeBytes(dir string) (uint64, error) {
	if _, err := Rel(dir); err != nil {
		return 0, err
	}
	return 0, errors.ErrUnsupported
}
