package pool

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type PoolConfig struct {
	//初始化连接池是最小连接数
	InitialCap int
	//连接池最大连接数
	MaxCap int
	//生成连接工厂
	Factory func() (interface{}, error)
	//关闭链接方法
	Close func(interface{}) error
	//链接最大空闲时间
	IdleTimeout time.Duration
}

type channelPool struct {
	mu          sync.Mutex
	conns       chan *idleConn
	factory     func() (interface{}, error)
	close       func(interface{}) error
	idleTimeout time.Duration
}

type idleConn struct {
	conn interface{}
	t    time.Time
}

func NewChannelPool(poolConfig *PoolConfig) (Pool, error) {
	if poolConfig.InitialCap < 0 || poolConfig.MaxCap <= 0 || poolConfig.InitialCap > poolConfig.MaxCap {
		return nil, errors.New("invalid capacity settings")
	}
	c := &channelPool{
		conns:       make(chan *idleConn, poolConfig.MaxCap),
		factory:     poolConfig.Factory,
		close:       poolConfig.Close,
		idleTimeout: poolConfig.IdleTimeout,
	}
	for i := 0; i < poolConfig.InitialCap; i++ {
		conn, err := c.factory()
		if err != nil {
			c.Release()
			return nil, fmt.Errorf("factory is not able to fill the pool: %s", err)
		}
		c.conns <- &idleConn{conn: conn, t: time.Now()}
	}
	return c, nil
}

func (c *channelPool) getConns() chan *idleConn {
	c.mu.Lock()
	conns := c.conns
	c.mu.Unlock()
	return conns
}

func (c *channelPool) Get() (interface{}, error) {
	conns := c.getConns()
	if conns == nil {
		return nil, ErrClosed
	}
	for {
		select {
		case wrapConn := <-conns:
			if wrapConn == nil {
				return nil, ErrClosed
			}
			//判断是否超时，超时则丢弃
			if timeout := c.idleTimeout; timeout > 0 {
				if wrapConn.t.Add(timeout).Before(time.Now()) {
					//丢弃并关闭该链接
					c.Close(wrapConn.conn)
					continue
				}
			}
			return wrapConn.conn, nil
		default:
			return nil, nil
		}
	}
}

func (c *channelPool) Put(conn interface{}) error {
	if conn == nil {
		return errors.New("connection is nil. rejecting")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conns == nil {
		return c.Close(conn)
	}
	select {
	case c.conns <- &idleConn{conn: conn, t: time.Now()}:
		return nil
	default:
		return c.Close(conn)
	}
}

func (c *channelPool) Close(conn interface{}) error {
	if conn == nil {
		return errors.New("connection is nil. rejecting")
	}
	return c.close(conn)
}

func (c *channelPool) Release() {
	c.mu.Lock()
	conns := c.conns
	c.conns = nil
	c.factory = nil
	closeFunc := c.close
	c.close = nil
	c.mu.Unlock()
	if conns == nil {
		return
	}
	close(conns)
	for wrapConn := range conns {
		closeFunc(wrapConn.conn)
	}
}

//Len 连接池中已有的连接
func (c *channelPool) Len() int {
	return len(c.getConns())
}
