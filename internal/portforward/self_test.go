package portforward

import "os"

// selfPID is defined in a separate file so manager_test.go reads cleanly.
func selfPID() int { return os.Getpid() }
