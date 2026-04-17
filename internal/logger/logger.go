package logger

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var globalLogger *zap.SugaredLogger

// Init 初始化日志系统
func Init(level, logFile string, console bool, maxSize, maxBackups, maxAge int) error {
	// 解析日志级别
	var zapLevel zapcore.Level
	switch level {
	case "DEBUG":
		zapLevel = zapcore.DebugLevel
	case "INFO":
		zapLevel = zapcore.InfoLevel
	case "WARN":
		zapLevel = zapcore.WarnLevel
	case "ERROR":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	// 创建日志目录
	if logFile != "" {
		logDir := filepath.Dir(logFile)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
	}

	// 配置编码器
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 创建核心
	var cores []zapcore.Core

	// 文件输出
	if logFile != "" {
		fileWriter := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   true,
		}
		fileCore := zapcore.NewCore(
			zapcore.NewJSONEncoder(encoderConfig),
			zapcore.AddSync(fileWriter),
			zapLevel,
		)
		cores = append(cores, fileCore)
	}

	// 控制台输出
	if console {
		consoleCore := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(os.Stdout),
			zapLevel,
		)
		cores = append(cores, consoleCore)
	}

	// 创建 logger
	core := zapcore.NewTee(cores...)
	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	globalLogger = logger.Sugar()

	return nil
}

// Debug 输出调试日志
func Debug(args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Debug(args...)
	}
}

// Debugf 格式化输出调试日志
func Debugf(template string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Debugf(template, args...)
	}
}

// Info 输出信息日志
func Info(args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Info(args...)
	}
}

// Infof 格式化输出信息日志
func Infof(template string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Infof(template, args...)
	}
}

// Warn 输出警告日志
func Warn(args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Warn(args...)
	}
}

// Warnf 格式化输出警告日志
func Warnf(template string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Warnf(template, args...)
	}
}

// Error 输出错误日志
func Error(args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Error(args...)
	}
}

// Errorf 格式化输出错误日志
func Errorf(template string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Errorf(template, args...)
	}
}

// Fatal 输出致命错误日志并退出
func Fatal(args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Fatal(args...)
	}
}

// Fatalf 格式化输出致命错误日志并退出
func Fatalf(template string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Fatalf(template, args...)
	}
}

// Sync 同步日志缓冲区
func Sync() {
	if globalLogger != nil {
		_ = globalLogger.Sync()
	}
}
