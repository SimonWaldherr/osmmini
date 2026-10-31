package osmmini

// Store queue candidates by value. A* can insert hundreds of thousands of
// candidates; allocating a pointer object for every insertion burdens the GC.
type routeValueHeap []pqItem

func (h *routeValueHeap) push(v pqItem) {
	*h = append(*h, v)
	i := len(*h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if (*h)[p].f <= v.f {
			break
		}
		(*h)[i] = (*h)[p]
		i = p
	}
	(*h)[i] = v
}
func (h *routeValueHeap) pop() pqItem {
	a := *h
	out := a[0]
	last := a[len(a)-1]
	a = a[:len(a)-1]
	if len(a) > 0 {
		i := 0
		for {
			child := 2*i + 1
			if child >= len(a) {
				break
			}
			if child+1 < len(a) && a[child+1].f < a[child].f {
				child++
			}
			if last.f <= a[child].f {
				break
			}
			a[i] = a[child]
			i = child
		}
		a[i] = last
	}
	*h = a
	return out
}
