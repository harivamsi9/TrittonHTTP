package tritonhttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	// Addr specifies the TCP address for the server to listen on,
	// in the form "host:port". It shall be passed to net.Listen()
	// during ListenAndServe().
	Addr string // e.g. ":0"

	// VirtualHosts contains a mapping from host name to the docRoot path
	// (i.e. the path to the directory to serve static files from) for
	// all virtual hosts that this server supports
	VirtualHosts map[string]string
}

// ListenAndServe listens on the TCP network address s.Addr and then
// handles requests on incoming connections.
func (s *Server) ListenAndServe() error {
	// s.ValidateServerSetup()

	listener, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}

	log.Printf("Listening at Address: %q", listener.Addr())

	defer func() {
		err = listener.Close()
		if err != nil {
			log.Printf("Error in closing listener: %q", err)
		}
	}()

	for {
		conn, err := listener.Accept() // Keeps looking for incoming connections continously
		if err != nil {
			return err
		}
		log.Printf("Accepted connection at: %q", conn.RemoteAddr())
		go s.HandleCurrentConnection(conn)
	}
}

func (s *Server) HandleCurrentConnection(conn net.Conn) {
	cur_buffer := bufio.NewReader(conn)

	for {
		// Setting up timeout
		timmer_now_5secs := time.Now().Add(time.Second * 5)
		if err := conn.SetReadDeadline(timmer_now_5secs); err != nil {
			log.Printf("Failed to set timeout for connection: %v", conn)
			_ = conn.Close()
			return
		}

		// Trying to read next request
		req, bytesReceived, err := ReadRequest(cur_buffer)

		//Handling EOF
		is_eof := errors.Is(err, io.EOF)
		if is_eof {
			log.Printf("Connection closed by: %v", conn.RemoteAddr())
			_ = conn.Close()
			return
		}

		// Handling timeout
		if err, ok := err.(net.Error); ok && err.Timeout() {
			no_bytesReceived := !bytesReceived
			if no_bytesReceived {
				log.Printf("Connection to %v timed out", conn.RemoteAddr())
				_ = conn.Close()
				return
			}
			res := &Response{}
			res.HandleInvalidBadRequest()
			_ = res.Write(conn)
			_ = conn.Close()
			return
		}

		if err != nil {
			log.Printf("Handle bad request for error: %v", err)
			res := &Response{}
			res.HandleInvalidBadRequest()
			_ = res.Write(conn)
			_ = conn.Close()
			return
		}

		log.Printf("Handle good request: %v", req)
		res := s.HandleValidGoodRequest(req)
		err = res.Write(conn)
		if err != nil {
			fmt.Println(err)
		}

		isReqClose := req.Close
		if isReqClose {
			_ = conn.Close()
			return
		}
	}
}

func (s *Server) HandleValidGoodRequest(req *Request) (res *Response) {
	res = &Response{}
	res.init(req)
	absPath := filepath.Join(s.VirtualHosts[req.Host], req.URL)
	left_absPath := absPath[:len(s.VirtualHosts[req.Host])]
	reqHostInVirtualHosts := s.VirtualHosts[req.Host]

	if left_absPath != reqHostInVirtualHosts {
		res.HandleNotFound(req)
	} else if _, err := os.Stat(absPath); errors.Is(err, os.ErrNotExist) {
		res.HandleNotFound(req)
	} else {
		res.HandleOK(req, absPath)
	}
	return res
}

func (res *Response) HandleOK(req *Request, path string) {
	res.StatusCode = 200
	res.FilePath = path

	stats, err := os.Stat(path)
	err_exists := errors.Is(err, os.ErrNotExist)
	if err_exists {
		log.Print(err)
	}
	res.Headers["Last-Modified"] = FormatTime(stats.ModTime())
	res.Headers["Content-Type"] = MIMETypeByExtension(filepath.Ext(path))
	res.Headers["Content-Length"] = strconv.FormatInt(stats.Size(), 10)
}

func (res *Response) HandleInvalidBadRequest() {
	res.init(nil)
	res.StatusCode = 400
	res.FilePath = ""
	res.Request = nil
	res.Headers["Connection"] = "close"
}

func (res *Response) HandleNotFound(req *Request) {
	res.StatusCode = 404
}

func (res *Response) init(req *Request) {
	res.Proto = "HTTP/1.1"
	res.Request = req
	res.Headers = make(map[string]string)
	res.Headers["Date"] = FormatTime(time.Now())
	if req != nil {
		lastChar_url := req.URL[len(req.URL)-1]
		if lastChar_url == '/' {
			req.URL = req.URL + "index.html"
		}
		isReqClose := req.Close
		if isReqClose {
			res.Headers["Connection"] = "close"
		}
	}
}

// func (s *Server) ValidateServerSetup() error {
// 	return nil
// }

// <-------------------------------- CODE RESPONSIBLE FOR RESPONSES --------------------------------->
func (res *Response) Write(w io.Writer) error {
	if err := res.WriteStatusLine(w); err != nil {
		return err
	}
	if err := res.WriteSortedHeaders(w); err != nil {
		return err
	}
	if err := res.WriteBody(w); err != nil {
		return err
	}
	return nil
}

func (res *Response) WriteStatusLine(w io.Writer) error {
	var statusCode string
	switch strconv.Itoa(res.StatusCode) {
	case "200":
		statusCode = "200 OK"
	case "400":
		statusCode = "400 Bad Request"
	case "404":
		statusCode = "404 Not Found"
	}

	statusLine := res.Proto + " " + statusCode + "\r\n"

	if _, err := w.Write([]byte(statusLine)); err != nil {
		return err
	}

	return nil
}

func (res *Response) WriteSortedHeaders(w io.Writer) error {
	list_Sorted_Keys := make([]string, 0, len(res.Headers))

	for eachKey, _ := range res.Headers {
		list_Sorted_Keys = append(list_Sorted_Keys, eachKey)
	}
	sort.Strings(list_Sorted_Keys)

	for _, eachKey := range list_Sorted_Keys {
		header := eachKey + ": " + res.Headers[eachKey] + "\r\n"
		if _, err := w.Write([]byte(header)); err != nil {
			return err
		}
	}
	if _, err := w.Write([]byte("\r\n")); err != nil {
		return err
	}

	return nil
}

func (res *Response) WriteBody(w io.Writer) error {
	var content []byte
	var err error
	res_path := res.FilePath
	if res_path != "" {
		if content, err = os.ReadFile(res.FilePath); err != nil {
			return err
		}
	}
	if _, err := w.Write(content); err != nil {
		return err
	}
	return nil
}

// <-------------------------------- CODE RELATED TO REQUEST MODULE --------------------------------->

func readCurrLine(cur_buffer *bufio.Reader) (string, error) {
	var curr_line string
	for {
		s, err := cur_buffer.ReadString('\n')
		curr_line += s
		if err != nil {
			return curr_line, err
		}
		isCRLFstrSuffix := strings.HasSuffix(curr_line, "\r\n")
		if isCRLFstrSuffix {
			// Striping the curr_line end
			curr_line = curr_line[:len(curr_line)-2]
			return curr_line, nil
		}
	}
}

func ReadRequest(cur_buffer *bufio.Reader) (req *Request, bytesReceived bool, err error) {
	req = &Request{}
	req.Headers = make(map[string]string)

	curr_line, err := readCurrLine(cur_buffer)
	if err != nil {
		return nil, false, err
	}

	req.Method, req.URL, req.Proto, err = parseEachRequestLine(curr_line)
	if err != nil {
		return nil, true, err
	}
	is_reqMethod := req.Method
	is_slash := req.URL[0]
	is_https1_1_proto := req.Proto
	if is_reqMethod != "GET" || is_slash != '/' || is_https1_1_proto != "HTTP/1.1" {
		return nil, true, fmt.Errorf("400")
	}

	hasHost := false
	req.Close = false
	for {
		curr_line, err := readCurrLine(cur_buffer)
		if err != nil {
			return nil, true, err
		}
		if curr_line == "" {
			break
		}
		fields := strings.SplitN(curr_line, ": ", 2)
		if len(fields) != 2 {
			return nil, true, fmt.Errorf("400")
		}
		eachKey := CanonicalHeaderKey(strings.TrimSpace(fields[0]))
		value := strings.TrimSpace(fields[1])

		if eachKey == "Host" {
			req.Host = value
			hasHost = true
		} else if eachKey == "Connection" && value == "close" {
			req.Close = true
		} else {
			req.Headers[eachKey] = value
		}
	}

	not_hasHost := !hasHost

	if not_hasHost {
		return nil, true, fmt.Errorf("400")
	}

	return req, true, nil
}

func parseEachRequestLine(curr_line string) (Method string, URL string, Proto string, err error) {
	fields := strings.SplitN(curr_line, " ", 3)
	if len(fields) != 3 {
		return "", "", "", fmt.Errorf("could not parse the request curr_line, got fields: %v", fields)
	}
	return fields[0], fields[1], fields[2], nil
}
