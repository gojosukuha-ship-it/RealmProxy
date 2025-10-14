package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"time"
)

const (
	ColorBlue   = "\033[34m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorReset  = "\033[0m"
	ColorYellow = "\x1b[33m"
)

var (
	logFilePath = "delorian/log.txt"
)

func Info(msg string) {
	date := time.Now().Format("15:04:05")
	info := fmt.Sprintf("%sINFO%s[%s] %s", ColorBlue, ColorReset, date, msg)
	fmt.Println(info)
}
func Infof(format string, args ...any) {
	date := time.Now().Format("15:04:05")
	formattedMsg := fmt.Sprintf(format, args...)
	infoOutput := fmt.Sprintf("%sINFO%s[%s] %s", ColorBlue, ColorReset, date, formattedMsg)
	fmt.Println(infoOutput)
}
func Debug(msg string) {
	date := time.Now().Format("15:04:05")
	debug := fmt.Sprintf("%sDEBUG%s[%s] %s", ColorGreen, ColorReset, date, msg)
	fmt.Println(debug)
}
func Debugf(format string, args ...any) {
	date := time.Now().Format("15:04:05")
	formattedMsg := fmt.Sprintf(format, args...)
	debugOutput := fmt.Sprintf("%sDEBUG%s[%s] %s", ColorGreen, ColorReset, date, formattedMsg)
	fmt.Println(debugOutput)
}
func Warn(msg string) {
	date := time.Now().Format("15:04:05")
	warn := fmt.Sprintf("%sWARN%s[%s] %s", ColorYellow, ColorReset, date, msg)
	fmt.Println(warn)
}
func Warnf(format string, args ...any) {
	date := time.Now().Format("15:04:05")
	formattedMsg := fmt.Sprintf(format, args...)
	warnOutput := fmt.Sprintf("%sWARN%s[%s] %s", ColorYellow, ColorReset, date, formattedMsg)
	fmt.Println(warnOutput)
}
func Error(msg string) {
	date := time.Now().Format("15:04:05")
	err := fmt.Sprintf("%sERROR%s[%s] %s", ColorRed, ColorReset, date, msg)
	fmt.Println(err)
	appendToLogFile(err)
	os.Exit(1)
}
func Errorf(format string, args ...any) {
	date := time.Now().Format("15:04:05")
	formattedMsg := fmt.Sprintf(format, args...)
	errOutput := fmt.Sprintf("%sERROR%s[%s] %s", ColorRed, ColorReset, date, formattedMsg)
	fmt.Println(errOutput)
	appendToLogFile(errOutput)
	os.Exit(1)
}
func appendToLogFile(msg string) {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	cleanMsg := re.ReplaceAllString(msg, "")

	entry := fmt.Sprintf("%s\n", cleanMsg)

	err := os.WriteFile(logFilePath, []byte(entry), os.ModeAppend|0644)
	if err != nil {
		log.Printf("[FATAL] could not write to %s: %v", logFilePath, err)
	}
}
