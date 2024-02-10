package main

import (
	"bufio"
	"fmt"
	"html"
	"io"
	"net/smtp"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var sendLogsMutex = &sync.Mutex{} // needed for concurrent access to the emailBuffer
var startupTime = time.Now()      // uses this time so we don't send emails if the app crashes while running for less than 1 minute
const runningTimeWindow = time.Minute * 2
const maxEmailBufferSize = 5
const maxLogBufferSize = 15

var version = "X.Y.Z"
var debug = os.Getenv("ERMON_DEBUG") == "true"
var emailsSent []time.Time
var finalRun bool = false
var timeSinceError time.Time
var emailBuffer [][]string
var logBuffer []string
var lastErrorLineIndex uint64 = 0

func sendLogsByEmail(cfg Config) {
	sendLogsMutex.Lock()

	// filter emailsSent to only include those within the last hour
	var newEmailsSent []time.Time
	for _, t := range emailsSent {
		if time.Since(t) < time.Hour {
			newEmailsSent = append(newEmailsSent, t)
		}
	}
	emailsSent = newEmailsSent

	if len(emailsSent) >= cfg.MaxEmailsPerHour {
		emailBuffer = nil
		sendLogsMutex.Unlock()
		return
	}

	if len(logBuffer) > 0 && (finalRun || (!timeSinceError.IsZero() && time.Since(timeSinceError) > runningTimeWindow)) {
		emailBuffer = append(emailBuffer, logBuffer)
		logBuffer = nil
	}

	// don't send email if the app has been running for less than 1 minute and then crashed
	if finalRun && time.Since(startupTime) < time.Minute && !debug {
		sendLogsMutex.Unlock()
		return
	}

	if len(emailBuffer) == 0 {
		sendLogsMutex.Unlock()
		return
	}

	// reset
	timeSinceError = time.Time{}
	lastErrorLineIndex = 0

	errorCount := 0
	errors := ""
	for i, buf := range emailBuffer {
		for _, line := range buf {
			if len(strings.TrimSpace(line)) == 0 {
				continue
			}
			if lineContainsError(cfg, line) {
				errors += "<span style=\"color: black\">" + html.EscapeString(line) + "</span>\n"
				errorCount++
			} else {
				errors += html.EscapeString(line) + "\n"
			}
		}
		if i < len(emailBuffer)-1 {
			errors += "<br />\n"
		}
	}

	emailBuffer = nil
	sendLogsMutex.Unlock()

	emailsSent = append(emailsSent, time.Now())
	sendMail(cfg, errors, errorCount)
}

func watchLogBuffer(cfg Config) {
	for {
		sendLogsByEmail(cfg)

		if finalRun {
			return
		}

		time.Sleep(time.Second)
	}
}

func readLogs(cfg Config, r io.Reader) {
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

		if lineContainsError(cfg, line) {
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

func lineContainsError(cfg Config, input string) bool {
	if cfg.IgnorePattern.MatchString(input) {
		return false
	}
	if cfg.MatchPattern.MatchString(input) {
		return true
	}
	return false
}

func sendMail(cfg Config, errors string, errorCount int) {
	smtpPort := "25"
	if cfg.SMTPPort != "" {
		smtpPort = cfg.SMTPPort
	}

	errorCountString := strconv.Itoa(errorCount)
	body := strings.Replace(mailTemplate, "{errors}", errors, -1)
	auth := smtp.PlainAuth("", cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPHost)
	recipients := []string{cfg.MailTo}
	message := []byte("From: " + cfg.MailFrom + "\r\n" +
		"To: " + cfg.MailTo + "\r\n" +
		"Subject: [" + cfg.AppName + "] You've got " + errorCountString + " errors\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		body + "\r\n")

	err := smtp.SendMail(cfg.SMTPHost+":"+smtpPort, auth, cfg.MailFrom, recipients, message)
	if err != nil {
		fmt.Println("[ermon] SendMail error:", err)
		return
	}
}

var mailTemplate = `
<html>
  <meta charset="utf-8" />
  <body style="background-color: #f4f5f6; font-family: sans-serif;">
		<div style="padding-top: 20px; font: bold italic 35px arial, sans-serif;
              	background-color: #b6bdc3; color: transparent; text-shadow: 1px 1px 1px rgba(255,255,255,0.5);
              	-webkit-background-clip: text; -moz-background-clip: text; background-clip: text; text-align: center;">
      ermon
    </div>
    <div style="padding: 30px;">
      <div style="background-color: #fff; padding: 20px; border-radius: 4px; font-size: 15px; color: #808080;">
        <pre style="font-family: monospace">{errors}</pre>
      </div>
      <div style="margin-top: 20px; padding: 10px; font-size: 15px; color: #9a9ea6; text-align: center;">
        This email alert was produced by
        <a href="https://github.com/gornostal/ermon" style="color: #9a9ea6; text-decoration: underline">ermon</a>.
      </div>
    </div>
  </body>
</html>
`

type Config struct {
	SMTPHost         string
	SMTPPort         string
	SMTPUsername     string
	SMTPPassword     string
	AppName          string
	MailFrom         string
	MailTo           string
	MaxEmailsPerHour int
	MatchPattern     *regexp.Regexp
	IgnorePattern    *regexp.Regexp
}

func parseConfig(filename string) (*Config, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("error opening config file: %s", err)
	}
	defer file.Close()

	cfg := &Config{}

	// read environment variables first
	cfg.SMTPHost = os.Getenv("SMTP_HOST")
	cfg.SMTPPort = os.Getenv("SMTP_PORT")
	cfg.SMTPUsername = os.Getenv("SMTP_USERNAME")
	cfg.SMTPPassword = os.Getenv("SMTP_PASSWORD")
	cfg.AppName = os.Getenv("ERMON_APP_NAME")
	cfg.MailFrom = os.Getenv("ERMON_MAIL_FROM")
	cfg.MailTo = os.Getenv("ERMON_MAIL_TO")
	matchPattern := os.Getenv("ERMON_MATCH_PATTERN")
	ignorePattern := os.Getenv("ERMON_IGNORE_PATTERN")
	maxEmailsPerHour := os.Getenv("ERMON_MAX_EMAILS_PER_HOUR")

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 || line[0] == '#' {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			// ignore invalid lines
			continue
		}

		// values from the config file take precedence
		switch strings.TrimSpace(parts[0]) {
		case "SMTP_HOST":
			cfg.SMTPHost = strings.TrimSpace(parts[1])
		case "SMTP_PORT":
			cfg.SMTPPort = strings.TrimSpace(parts[1])
		case "SMTP_USERNAME":
			cfg.SMTPUsername = strings.TrimSpace(parts[1])
		case "SMTP_PASSWORD":
			cfg.SMTPPassword = strings.TrimSpace(parts[1])
		case "ERMON_APP_NAME":
			cfg.AppName = strings.TrimSpace(parts[1])
		case "ERMON_MAIL_FROM":
			cfg.MailFrom = strings.TrimSpace(parts[1])
		case "ERMON_MAIL_TO":
			cfg.MailTo = strings.TrimSpace(parts[1])
		case "ERMON_MATCH_PATTERN":
			matchPattern = strings.TrimSpace(parts[1])
		case "ERMON_IGNORE_PATTERN":
			ignorePattern = strings.TrimSpace(parts[1])
		case "ERMON_MAX_EMAILS_PER_HOUR":
			maxEmailsPerHour = strings.TrimSpace(parts[1])
		}
	}

	// validate all fields are present in the loop
	for k, v := range map[string]string{
		"SMTP_HOST":           cfg.SMTPHost,
		"ERMON_MAIL_FROM":     cfg.MailFrom,
		"ERMON_MAIL_TO":       cfg.MailTo,
		"ERMON_APP_NAME":      cfg.AppName,
		"ERMON_MATCH_PATTERN": matchPattern,
	} {
		if len(v) == 0 {
			return nil, fmt.Errorf("missing required config value: %s", k)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	cfg.MaxEmailsPerHour = 5 // default
	if maxEmailsPerHour != "" {
		cfg.MaxEmailsPerHour, err = strconv.Atoi(maxEmailsPerHour)
		if err != nil {
			return cfg, fmt.Errorf("error converting ERMON_MAX_EMAILS_PER_HOUR to integer: %s", err)
		}
	}

	if matchPattern != "" {
		var err error
		cfg.MatchPattern, err = regexp.Compile(matchPattern)
		if err != nil {
			return cfg, fmt.Errorf("error compiling ERMON_MATCH_PATTERN: %s", err)
		}
	}

	if ignorePattern != "" {
		var err error
		cfg.IgnorePattern, err = regexp.Compile(ignorePattern)
		if err != nil {
			return cfg, fmt.Errorf("error compiling ERMON_IGNORE_PATTERN: %s", err)
		}
	}

	return cfg, nil
}

func main() {
	var cfgPath = ".ermon"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]

		if cfgPath == "-h" || cfgPath == "--help" || cfgPath == "version" {
			fmt.Println("ermon v" + version + " by Oleksandr Gornostal")
			fmt.Println("\033[37mFor usage and configuration, see https://github.com/gornostal/ermon\033[0m")
			os.Exit(0)
		}
	}

	config, err := parseConfig(cfgPath)
	if err != nil {
		fmt.Println("[ermon] ", err)
		os.Exit(1)
	}

	go watchLogBuffer(*config)

	readLogs(*config, os.Stdin)

	finalRun = true
	sendLogsByEmail(*config)
}
