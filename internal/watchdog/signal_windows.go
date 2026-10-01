package watchdog

import "os"

// Windows has no SIGTERM for GUI processes; the child is killed. The next
// launch cleans up a system proxy left pointing at the client.
func interrupt(p *os.Process) error { return p.Kill() }
