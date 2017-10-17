package socks5

import (
	"bufio"
	"fmt"
	"io"
	"net"

	"gitlab.com/tabjy/groundhog/pkg/util"
	"gitlab.com/tabjy/groundhog/pkg/crypto"
)

type socksConn struct {
	conn        net.Conn
	req         io.Reader
	res         io.Writer
	authMethods []byte
	cmd         byte
	destAddr    *util.Addr
}

func newSocksConn(conn net.Conn) *socksConn {
	writer, _ := conn.(io.Writer)
	return &socksConn{
		conn: conn,
		req:  bufio.NewReader(conn),
		res:  writer,
	}
}

func handleConn(conn net.Conn, args ...*interface{}) error {
	return newSocksConn(conn).serve()
}

// any connection-level fatal error should be returned for logging
func (c *socksConn) serve() error {
	defer c.conn.Close()

	// first request: authentication
	if err := c.checkSocksVer(); err != nil {
		return err
	}

	if err := c.auth(); err != nil {
		return err
	}

	if err := c.checkSocksVer(); err != nil {
		return err
	}

	if err := c.readCmd(); err != nil {
		return err
	}

	// check RSV byte, which MUST be 0x00
	rsv := []byte{0}
	if _, err := c.req.Read(rsv); err != nil {
		return err
	}
	if rsv[0] != 0x00 {
		return fmt.Errorf(util.ERR_TPL_SOCKS_ILLEGAL_RSV_FIELD, rsv[0])
	}

	if err := c.readDestAddr(); err != nil {
		return err
	}

	c.exec()

	return nil
}

func (c *socksConn) checkSocksVer() error {
	ver := []byte{0}
	if _, err := c.req.Read(ver); err != nil {
		return err
	}

	// supports SOCKS5 only
	if ver[0] != byte(0x05) {
		return fmt.Errorf(util.ERR_TPL_SOCKS_UNSUPPORTED_VER, ver[0])
	}

	return nil
}

// support "NO AUTHENTICATION REQUIRED" only. others no needed.
func (c *socksConn) auth() error {
	methodLen := []byte{0}
	if _, err := c.req.Read(methodLen); err != nil {
		return err
	}

	c.authMethods = make([]byte, int(methodLen[0]))
	if _, err := io.ReadAtLeast(c.req, c.authMethods, int(methodLen[0])); err != nil {
		return err
	}

	for method := range c.authMethods {
		if method == 0x00 {
			c.res.Write([]byte{0x05, util.SOCKS_AUTH_NO_AUTH})
			return nil
		}
	}

	c.res.Write([]byte{0x05, util.SOCKS_AUTH_USER_NO_ACCEPTABLE})
	return fmt.Errorf(util.ERR_TPL_SOCKS_NO_ACCEPTABLE_AUTH_METHOD)
}

func (c *socksConn) readCmd() error {
	cmd := []byte{0}
	if _, err := c.req.Read(cmd); err != nil {
		return err
	}

	switch cmd[0] {
	case util.SOCKS_CMD_CONNECT, util.SOCKS_CMD_BIND, util.SOCKS_CMD_UDP_ASSOCIATE:
		c.cmd = cmd[0]
		return nil

	default:
		return fmt.Errorf(util.ERR_TPL_SOCKS_UNSUPPORTED_CMD, cmd[0])
	}
}

func (c *socksConn) readDestAddr() error {
	addr, err := util.NewAddr().Parse(c.req)
	if err != nil {
		// TODO: handel unsupported address type response
		c.writeReply(util.REP_ADDR_TYP_NOT_SUPPORTED, util.NewAddr())
		return err
	}
	c.destAddr = addr
	return nil
}

// support CONNECT only, at least for this moment
func (c *socksConn) exec() error {
	// TODO: add support for BIND and UDPAssociate
	switch c.cmd {
	case util.SOCKS_CMD_CONNECT:
		// TODO: implement bypass whitelist

		// TODO: connect proxy server
		target, err := net.Dial("tcp", c.destAddr.String())
		if err != nil {
			c.writeReply(util.REP_GENERAL_FAILURE, util.NewAddr())
			return err
		}

		if err := c.writeReply(util.REP_SUCCEEDED, c.destAddr); err != nil {
			return err
		}

		errCh := make(chan error, 2)

		/*
		go proxy(target, c.req, errCh)
		go proxy(c.res, target, errCh)

		for i := 0; i < 2; i++ {
			err := <-errCh
			if err != nil {
				// return from this function closes target (and conn).
				return err
			}
		}
		*/

		// proof of concept, to be deleted
		plainSide, cipherSide, err := crypto.CreateAESCFBPipe([]byte("1234567890ABCDEF"), errCh)
		plainSide2, cipherSide2, err := crypto.CreateAESCFBPipe([]byte("1234567890ABCDEF"), errCh)

		if err != nil {
			c.writeReply(util.REP_GENERAL_FAILURE, util.NewAddr())
			return err
		}

		// uploading direction
		go func() {
			io.Copy(plainSide, c.req)
		}()
		go func() {
			io.Copy(cipherSide2, cipherSide)
		}()
		go func() {
			io.Copy(target, plainSide2)
		}()

		// downloading direction
		go func() {
			io.Copy(plainSide2, target)
		}()
		go func() {
			io.Copy(cipherSide, cipherSide2)
		}()
		go func() {
			io.Copy(c.res, plainSide)
		}()


		<-errCh

	default:
		return fmt.Errorf(util.ERR_TPL_SOCKS_UNSUPPORTED_CMD, c.cmd)
	}
	return nil
}

func (c *socksConn) writeReply(rep byte, addr *util.Addr) error {
	addrBytes, err := addr.Build()
	if err != nil {
		return err
	}

	buf := make([]byte, 3+len(addrBytes))
	buf[0] = 0x05
	buf[1] = rep
	buf[2] = 0x00
	copy(buf[3:], addrBytes)

	if _, err := c.res.Write(buf); err != nil {
		return err
	}
	return nil
}

type closeWriter interface {
	CloseWrite() error
}

func proxy(dst io.Writer, src io.Reader, errCh chan error) {
	_, err := io.Copy(dst, src)
	if tcpConn, ok := dst.(closeWriter); ok {
		tcpConn.CloseWrite()
	}
	errCh <- err
}
