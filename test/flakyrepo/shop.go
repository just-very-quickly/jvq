// Package shop is a stand-in for a real package under test.
package shop

// Total adds up prices in cents.
func Total(prices ...int) int {
	t := 0
	for _, p := range prices {
		t += p
	}
	return t
}
