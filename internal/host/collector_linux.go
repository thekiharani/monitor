package host

func newHostCollector(proc, sys, root string) hostCollector {
	return NewSystemCollector(proc, sys, root)
}
