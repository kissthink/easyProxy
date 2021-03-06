package utils

import (
	"encoding/base64"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	COMPRESS_NONE_ENCODE = iota
	COMPRESS_NONE_DECODE
	COMPRESS_SNAPY_ENCODE
	COMPRESS_SNAPY_DECODE
	VERIFY_EER        = "vkey"
	WORK_MAIN         = "main"
	WORK_CHAN         = "chan"
	RES_SIGN          = "sign"
	RES_MSG           = "msg0"
	RES_CLOSE         = "clse"
	CONN_SUCCESS      = "sucs"
	CONN_ERROR        = "fail"
	TEST_FLAG         = "tst"
	CONN_TCP          = "tcp"
	CONN_UDP          = "udp"
	UnauthorizedBytes = `HTTP/1.1 401 Unauthorized
Content-Type: text/plain; charset=utf-8
WWW-Authenticate: Basic realm="easyProxy"

401 Unauthorized`
	IO_EOF              = "PROXYEOF"
	ConnectionFailBytes = `HTTP/1.1 404 Not Found

`
)

//copy
func Relay(in, out net.Conn, compressType int, crypt, mux bool, rate *Rate) (n int64, err error) {
	switch compressType {
	case COMPRESS_SNAPY_ENCODE:
		n, err = copyBuffer(NewSnappyConn(in, crypt, rate), out)
		out.Close()
		NewSnappyConn(in, crypt, rate).Write([]byte(IO_EOF))
	case COMPRESS_SNAPY_DECODE:
		n, err = copyBuffer(in, NewSnappyConn(out, crypt, rate))
		in.Close()
		if !mux {
			out.Close()
		}
	case COMPRESS_NONE_ENCODE:
		n, err = copyBuffer(NewCryptConn(in, crypt, rate), out)
		out.Close()
		NewCryptConn(in, crypt, rate).Write([]byte(IO_EOF))
	case COMPRESS_NONE_DECODE:
		n, err = copyBuffer(in, NewCryptConn(out, crypt, rate))
		in.Close()
		if !mux {
			out.Close()
		}
	}
	return
}

//判断压缩方式
func GetCompressType(compress string) (int, int) {
	switch compress {
	case "":
		return COMPRESS_NONE_DECODE, COMPRESS_NONE_ENCODE
	case "snappy":
		return COMPRESS_SNAPY_DECODE, COMPRESS_SNAPY_ENCODE
	default:
		log.Fatalln("数据压缩格式错误")
	}
	return COMPRESS_NONE_DECODE, COMPRESS_NONE_ENCODE
}

//通过host获取对应的ip地址
func GetHostByName(hostname string) string {
	if !DomainCheck(hostname) {
		return hostname
	}
	ips, _ := net.LookupIP(hostname)
	if ips != nil {
		for _, v := range ips {
			if v.To4() != nil {
				return v.String()
			}
		}
	}
	return ""
}

//检查是否是域名
func DomainCheck(domain string) bool {
	var match bool
	IsLine := "^((http://)|(https://))?([a-zA-Z0-9]([a-zA-Z0-9\\-]{0,61}[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,6}(/)"
	NotLine := "^((http://)|(https://))?([a-zA-Z0-9]([a-zA-Z0-9\\-]{0,61}[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,6}"
	match, _ = regexp.MatchString(IsLine, domain)
	if !match {
		match, _ = regexp.MatchString(NotLine, domain)
	}
	return match
}

//检查basic认证
func CheckAuth(r *http.Request, user, passwd string) bool {
	s := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(s) != 2 {
		return false
	}

	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return false
	}

	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return false
	}
	return pair[0] == user && pair[1] == passwd
}

//get bool by str
func GetBoolByStr(s string) bool {
	switch s {
	case "1", "true":
		return true
	}
	return false
}

//get str by bool
func GetStrByBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

//int
func GetIntNoErrByStr(str string) int {
	i, _ := strconv.Atoi(str)
	return i
}

// io.copy的优化版，读取buffer长度原为32*1024，与snappy不同，导致读取出的内容存在差异，不利于解密
//内存优化 用到pool，快速回收
func copyBuffer(dst io.Writer, src io.Reader) (written int64, err error) {
	for {
		//放在里面是为了加快回收和重利用
		buf := bufPoolCopy.Get().([]byte)
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			bufPoolCopy.Put(buf)
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		} else {
			bufPoolCopy.Put(buf)
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}

//连接重置 清空缓存区
func FlushConn(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(time.Second * 3))
	buf := bufPool.Get().([]byte)
	defer bufPool.Put(buf)
	for {
		if _, err := c.Read(buf); err != nil {
			break
		}
	}
	c.SetReadDeadline(time.Time{})
}

//简单的一个校验值
func Getverifyval(vkey string) string {
	return Md5(vkey)
}

//wait replay group
//conn1 网桥 conn2
func ReplayWaitGroup(conn1 net.Conn, conn2 net.Conn, compressEncode, compressDecode int, crypt, mux bool, rate *Rate) (out int64, in int64) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		in, _ = Relay(conn1, conn2, compressEncode, crypt, mux, rate)
		wg.Done()
	}()
	out, _ = Relay(conn2, conn1, compressDecode, crypt, mux, rate)
	wg.Wait()
	return
}

func ChangeHostAndHeader(r *http.Request, host string, header string, addr string) {
	if host != "" {
		r.Host = host
	}
	if header != "" {
		h := strings.Split(header, "\n")
		for _, v := range h {
			hd := strings.Split(v, ":")
			if len(hd) == 2 {
				r.Header.Set(hd[0], hd[1])
			}
		}
	}
	addr = strings.Split(addr, ":")[0]
	r.Header.Set("X-Forwarded-For", addr)
	r.Header.Set("X-Real-IP", addr)
}

func ReadAllFromFile(filePth string) ([]byte, error) {
	f, err := os.Open(filePth)
	if err != nil {
		return nil, err
	}
	return ioutil.ReadAll(f)
}
