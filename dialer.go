package ubernet

import (
	"context"
	"net"
	"syscall"
	"time"
)

// A Dialer contains options for connecting to an address.
//
// The zero value for each field is equivalent to dialing
// without that option. Dialing with the zero value of Dialer
// is therefore equivalent to just calling the Dial function.
type Dialer struct {
	Timeout       time.Duration
	Deadline      time.Time
	LocalAddr     net.Addr
	DualStack     bool
	FallbackDelay time.Duration
	KeepAlive     time.Duration
	Resolver      *net.Resolver
	Cancel        <-chan struct{}
	Control       func(network, address string, c syscall.RawConn) error
}

func (d *Dialer) resolver() *net.Resolver {
	if d.Resolver != nil {
		return d.Resolver
	}
	return net.DefaultResolver
}

// DialTCP ..
func (d *Dialer) DialTCP(ctx context.Context, network, address string) (net.Conn, error) {

	// addrs, err := d.resolver().resolveAddrList(ctx, "dial", network, address, d.LocalAddr)
	// if err != nil {
	// 	return nil, &OpError{Op: "dial", Net: network, Source: nil, Addr: nil, Err: err}
	// }

	// _, err := createSocket(ctx, network, d.LocalAddr, syscall.SOCK_STREAM, 0)
	// if err != nil {
	// 	panic(err)
	// }
	return nil, nil
}
