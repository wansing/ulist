package util

// Removes all occurrences of element in slice. Returns true if occurrences were found and removed.
func RemoveElement(slice *[]string, element string) (removed bool) {
	i := 0 // result index
	for _, s := range *slice {
		if s == element {
			removed = true
		} else { // keep
			(*slice)[i] = s
			i++
		}
	}
	(*slice) = (*slice)[:i]
	return
}
