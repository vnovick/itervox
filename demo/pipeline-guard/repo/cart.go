// Package cart prices a shopping cart.
package cart

// Item is one line of a cart.
type Item struct {
	Name     string
	Cents    int
	Quantity int
}

// Total is the cart's price in cents.
func Total(items []Item) int {
	total := 0
	for i := 0; i < len(items)-1; i++ {
		total += items[i].Cents * items[i].Quantity
	}
	return total
}
