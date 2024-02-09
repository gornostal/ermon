package main

import (
	"bufio"
	"fmt"
	"io"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"
)

var mutex = &sync.Mutex{}    // needed for concurrent access to the emailBuffer
var startupTime = time.Now() // uses this time so we don't send emails if the app crashes while running for less than 1 minute
const runningTimeWindow = time.Minute / 4
const maxEmailBufferSize = 5
const maxLogBufferSize = 15

var finalRun bool = false
var timeSinceError time.Time
var emailBuffer [][]string
var logBuffer []string
var lastErrorLineIndex uint64 = 0

func sendLogsByEmail() {
	mutex.Lock()

	if len(logBuffer) > 0 && (finalRun || (!timeSinceError.IsZero() && time.Since(timeSinceError) > runningTimeWindow)) {
		emailBuffer = append(emailBuffer, logBuffer)
		logBuffer = nil
	}

	// don't send email if the app has been running for less than 1 minute and then crashed
	// if finalRun && time.Since(startupTime) < time.Minute {
	// 	mutex.Unlock()
	// 	return
	// }

	if len(emailBuffer) == 0 {
		mutex.Unlock()
		return
	}

	// reset
	timeSinceError = time.Time{}
	lastErrorLineIndex = 0

	errors := ""
	for i, buf := range emailBuffer {
		for _, line := range buf {
			if len(strings.TrimSpace(line)) == 0 {
				continue
			}
			if strings.Contains(line, "error") {
				errors += "<b>" + line + "</b>\n"
			} else {
				errors += line + "\n"
			}
		}
		if i < len(emailBuffer)-1 {
			errors += "<br />\n"
		}
	}

	emailBuffer = nil
	mutex.Unlock()

	sendErrorsByEmail(errors)
}

func watchLogBuffer() {
	for {
		sendLogsByEmail()

		if finalRun {
			return
		}

		time.Sleep(time.Second)
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
				logBuffer = nil
			}
			continue
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
					logBuffer = nil
				}
				logBuffer = append(logBuffer, last3lines[:]...)
				logBuffer = append(logBuffer, line)
			}

			lastErrorLineIndex = i
		}

		// maintain a buffer of last 3 lines
		if len(last3lines) >= 3 {
			copy(last3lines[:], last3lines[1:])
			last3lines[2] = line
		} else {
			last3lines[len(logBuffer)] = line
		}

		// add a bit of context after an error
		if lastErrorLineIndex > 0 && lastErrorLineIndex != i && (i-lastErrorLineIndex) <= contextSize {
			logBuffer = append(logBuffer, line)
		}

	}

	if err := scanner.Err(); err != nil {
		fmt.Println("[ermon] Scanner error:", err)
	}
}

func sendErrorsByEmail(errors string) {
	from := "xxx"
	password := "xxx"
	to := "xxx"
	appname := "xxx"
	smtpHost := "email-smtp.us-east-1.amazonaws.com"
	smtpPort := "587"
	body := strings.Replace(mailTemplate, "{errors}", errors, -1)
	message := []byte("From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: [" + appname + "] You've got XX errors in the last YY min\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		body + "\r\n")

	auth := smtp.PlainAuth("", "awsusername", password, smtpHost)

	err := smtp.SendMail(smtpHost+":"+smtpPort, auth, from, []string{to}, message)
	if err != nil {
		fmt.Println("[ermon] SendMail error:", err)
		return
	}
}

var mailTemplate = `
<html>
  <meta charset="utf-8" />
  <body style="background-color: #f4f5f6; font-family: sans-serif;">
    <div style="margin-top: 30px; font: bold italic 35px arial, sans-serif;
                background-color: #b6bdc3; color: transparent; text-shadow: 1px 1px 1px rgba(255,255,255,0.5);
                -webkit-background-clip: text; -moz-background-clip: text; background-clip: text; text-align: center;">
      ermon
    </div>
    <div style="margin: 0; background-color: #f4f5f6; padding: 30px;">
      <div
        style="background-color: #fff; padding: 20px; border-radius: 4px; font-size: 15px; color: #333;">
        <pre style="font-family: monospace">
{errors}
        </pre>
      </div>
      <div style="margin-top: 20px; padding: 10px; font-size: 15px; color: #9a9ea6; text-align: center;">
        This email alert was produced by
        <a href="https://github.com/gornostal/ermon" style="color: #9a9ea6; text-decoration: underline">ermon</a>.
      </div>
    </div>
  </body>
</html>
`

func main() {
	go watchLogBuffer()

	readLogs(os.Stdin)

	finalRun = true
	sendLogsByEmail()
}
