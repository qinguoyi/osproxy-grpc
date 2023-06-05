package bootstrap

import (
	"github.com/qinguoyi/ObjectStorageProxy/config"
	"github.com/qinguoyi/ObjectStorageProxy/utils"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const loggerKey = iota

var (
	level    zapcore.Level // zap 日志等级
	options  []zap.Option  // zap 配置项
	lgLogger = new(LangGoLogger)
)

// LangGoLogger 自定义Logger结构
type LangGoLogger struct {
	Logger *zap.Logger
	Once   *sync.Once
}

// newLangGoLogger .
func newLangGoLogger() *LangGoLogger {
	return &LangGoLogger{
		Logger: &zap.Logger{},
		Once:   &sync.Once{},
	}
}

// NewLogger 生成新Logger
func NewLogger() *LangGoLogger {
	if lgLogger.Logger != nil {
		return lgLogger
	} else {
		lgLogger = newLangGoLogger()
		lgLogger.initLangGoLogger(lgConfig.Conf)
		return lgLogger
	}
}

// initLangGoLogger 初始化全局log
func (lg *LangGoLogger) initLangGoLogger(conf *config.Configuration) {
	lg.Once.Do(
		func() {
			lg.Logger = initializeLog(conf)
		},
	)
}

func initializeLog(conf *config.Configuration) *zap.Logger {
	// 创建根目录
	createRootDir(conf)

	// 设置日志等级
	setLogLevel(conf)

	if conf.Log.ShowLine {
		options = append(options, zap.AddCaller())
	}

	// 初始化zap
	return zap.New(getZapCore(conf), options...)
}

func createRootDir(conf *config.Configuration) {
	logFileDir := conf.Log.RootDir
	if !filepath.IsAbs(logFileDir) {
		logFileDir = filepath.Join(rootPath, logFileDir)
	}

	if ok, _ := utils.Exists(logFileDir); !ok {
		_ = os.Mkdir(conf.Log.RootDir, os.ModePerm)
	}
}

func setLogLevel(conf *config.Configuration) {
	switch conf.Log.Level {
	case "debug":
		level = zap.DebugLevel
		options = append(options, zap.AddStacktrace(level))
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
		options = append(options, zap.AddStacktrace(level))
	case "dpanic":
		level = zap.DPanicLevel
	case "panic":
		level = zap.PanicLevel
	case "fatal":
		level = zap.FatalLevel
	default:
		level = zap.InfoLevel
	}
}

func getZapCore(conf *config.Configuration) zapcore.Core {
	var encoder zapcore.Encoder

	// 调整编码器默认配置 输出内容
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = func(time time.Time, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(time.Format("[" + "2006-01-02 15:04:05.000" + "]"))
	}
	encoderConfig.EncodeLevel = func(l zapcore.Level, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(conf.App.Env + "." + l.String())
	}

	// 设置编码器，日志的输出格式
	if conf.Log.Format == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// 同时输出到控制台和文件
	var multiWS zapcore.WriteSyncer
	if conf.Log.EnableFile {
		multiWS = zapcore.NewMultiWriteSyncer(getLogWriter(conf), zapcore.AddSync(os.Stdout))
	} else {
		multiWS = zapcore.AddSync(os.Stdout)
	}

	return zapcore.NewCore(encoder, multiWS, level)
}

// 使用 lumberjack 作为日志写入器
func getLogWriter(conf *config.Configuration) zapcore.WriteSyncer {
	file := &lumberjack.Logger{
		Filename:   conf.Log.RootDir + "/" + conf.Log.Filename,
		MaxSize:    conf.Log.MaxSize,
		MaxBackups: conf.Log.MaxBackups,
		MaxAge:     conf.Log.MaxAge,
		Compress:   conf.Log.Compress,
	}
	return zapcore.AddSync(file)
}
