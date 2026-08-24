package tui

// viewProbe lets tests read model state inside the program's event loop
// (the model's fields belong to the program goroutine; direct reads from a
// test would race). Update handles it before the real switch.
type viewProbe struct{ fn func(*model) }
