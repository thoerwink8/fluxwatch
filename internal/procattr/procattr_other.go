//go:build !darwin && !windows

package procattr

type otherPlatform struct{}

func platform() Platform { return otherPlatform{} }

func (otherPlatform) PortPIDs() (map[int]int, error) { return nil, ErrUnsupported }
func (otherPlatform) AppNames([]int) map[int]string  { return nil }
