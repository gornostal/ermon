package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var mutex = &sync.Mutex{}
var startupTime = time.Now() // todo: don't send emails if the app has been running for less than 1 minute
const runningTimeWindow = time.Minute / 4
const maxEmailBufferSize = 5
const maxLogBufferSize = 15

var lastRun bool = false
var timeSinceError time.Time
var emailBuffer [][]string
var logBuffer []string
var lastErrorLineIndex uint64 = 0

func sendLogsByEmail() {
	mutex.Lock()

	if len(logBuffer) > 0 && (lastRun || (!timeSinceError.IsZero() && time.Since(timeSinceError) > runningTimeWindow)) {
		emailBuffer = append(emailBuffer, logBuffer)
		logBuffer = logBuffer[:0]
	}

	if len(emailBuffer) == 0 {
		mutex.Unlock()
		// fmt.Println("(No logs to send)")
		return
	}

	// reset
	timeSinceError = time.Time{}
	localEmailBuffer := make([][]string, len(emailBuffer))
	copy(localEmailBuffer, emailBuffer)
	emailBuffer = emailBuffer[:0]
	lastErrorLineIndex = 0

	mutex.Unlock()

	fmt.Println("...Sending email...")
	for _, buf := range localEmailBuffer {
		for _, line := range buf {
			if len(strings.TrimSpace(line)) == 0 {
				continue
			}
			fmt.Println(line)
		}
		fmt.Println("...")
	}
	fmt.Println("...End of email...")

}

func watchLogBuffer() {
	for {
		sendLogsByEmail()

		if lastRun {
			return
		}

		time.Sleep(time.Second * 1)
	}
}

func readLogs(r io.Reader) {
	scanner := bufio.NewScanner(r)
	const contextSize = 3
	var i uint64 = 0 // line number
	var last3lines [contextSize]string

	for scanner.Scan() {
		i++
		line := scanner.Text()
		fmt.Println(line)

		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		var bufferIsFull bool = len(emailBuffer) >= maxEmailBufferSize || len(logBuffer) >= maxLogBufferSize
		if bufferIsFull {
			// wait for the logBuffers to be consumed
			if len(logBuffer) > 0 {
				logBuffer = logBuffer[:0]
			}
			continue
		}

		// maintain a buffer of last 3 lines
		if len(last3lines) >= 3 {
			copy(last3lines[:], last3lines[1:])
			last3lines[2] = line
		} else {
			last3lines[len(logBuffer)] = line
		}

		// add a bit of context after an error
		if lastErrorLineIndex > 0 && (i-lastErrorLineIndex) <= contextSize {
			logBuffer = append(logBuffer, line)
		}

		if strings.Contains(line, "error") {
			// record the time so we can track number of errors per configured time period
			// this time will be reset when email is sent
			if timeSinceError.IsZero() {
				timeSinceError = time.Now()
			}

			if lastErrorLineIndex == 0 || (i-lastErrorLineIndex) > contextSize {
				if len(logBuffer) > 0 {
					emailBuffer = append(emailBuffer, logBuffer)
					logBuffer = logBuffer[:0]
				}
				logBuffer = append(logBuffer, last3lines[:]...)
			}

			lastErrorLineIndex = i
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("[logart] Scanner error:", err)
	}
}

func main() {
	go watchLogBuffer()

	readLogs(os.Stdin)

	lastRun = true
	sendLogsByEmail()
}
