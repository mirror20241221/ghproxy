package proxy

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"ghproxy/config"
	"io"
	"net/url"
	"regexp"
	"strings"
)

func Matcher(rawPath string, cfg *config.Config) (string, string, string, *GHProxyErrors) {
	var (
		user    string
		repo    string
		matcher string
	)
	// 匹配 "https://github.com"开头的链接
	if strings.HasPrefix(rawPath, "https://github.com") {
		remainingPath := strings.TrimPrefix(rawPath, "https://github.com")
		if strings.HasPrefix(remainingPath, "/") {
			remainingPath = strings.TrimPrefix(remainingPath, "/")
		}
		// 预期格式/user/repo/more...
		// 取出user和repo和最后部分
		parts := strings.Split(remainingPath, "/")
		if len(parts) <= 2 {
			errMsg := "Not enough parts in path after matching 'https://github.com*'"
			return "", "", "", NewErrorWithStatusLookup(400, errMsg)
		}
		user = parts[0]
		repo = parts[1]
		// 匹配 "https://github.com"开头的链接
		if len(parts) >= 3 {
			switch parts[2] {
			case "releases", "archive":
				matcher = "releases"
			case "blob":
				matcher = "blob"
			case "raw":
				matcher = "raw"
			case "info", "git-upload-pack":
				matcher = "clone"
			default:
				errMsg := "Url Matched 'https://github.com*', but didn't match the next matcher"
				return "", "", "", NewErrorWithStatusLookup(400, errMsg)
			}
		}
		return user, repo, matcher, nil
	}
	// 匹配 "https://raw"开头的链接
	if strings.HasPrefix(rawPath, "https://raw") {
		remainingPath := strings.TrimPrefix(rawPath, "https://")
		parts := strings.Split(remainingPath, "/")
		if len(parts) <= 3 {
			errMsg := "URL after matched 'https://raw*' should have at least 4 parts (user/repo/branch/file)."
			return "", "", "", NewErrorWithStatusLookup(400, errMsg)
		}
		user = parts[1]
		repo = parts[2]
		matcher = "raw"

		return user, repo, matcher, nil
	}
	// 匹配 "https://gist"开头的链接
	if strings.HasPrefix(rawPath, "https://gist") {
		remainingPath := strings.TrimPrefix(rawPath, "https://")
		parts := strings.Split(remainingPath, "/")
		if len(parts) <= 3 {
			errMsg := "URL after matched 'https://gist*' should have at least 4 parts (user/gist_id)."
			return "", "", "", NewErrorWithStatusLookup(400, errMsg)
		}
		user = parts[1]
		repo = ""
		matcher = "gist"
		return user, repo, matcher, nil
	}
	// 匹配 "https://api.github.com/"开头的链接
	if strings.HasPrefix(rawPath, "https://api.github.com/") {
		matcher = "api"
		remainingPath := strings.TrimPrefix(rawPath, "https://api.github.com/")

		parts := strings.Split(remainingPath, "/")
		if parts[0] == "repos" {
			user = parts[1]
			repo = parts[2]
		}
		if parts[0] == "users" {
			user = parts[1]
		}
		if !cfg.Auth.ForceAllowApi {
			if cfg.Auth.Method != "header" || !cfg.Auth.Enabled {
				//return "", "", "", ErrAuthHeaderUnavailable
				errMsg := "AuthHeader Unavailable, Need to open header auth to enable api proxy"
				return "", "", "", NewErrorWithStatusLookup(403, errMsg)
			}
		}
		return user, repo, matcher, nil
	}
	//return "", "", "", ErrNotFound
	errMsg := "Didn't match any matcher"
	return "", "", "", NewErrorWithStatusLookup(404, errMsg)
}

func EditorMatcher(rawPath string, cfg *config.Config) (bool, error) {
	// 匹配 "https://github.com"开头的链接
	if strings.HasPrefix(rawPath, "https://github.com") {
		return true, nil
	}
	// 匹配 "https://raw.githubusercontent.com"开头的链接
	if strings.HasPrefix(rawPath, "https://raw.githubusercontent.com") {
		return true, nil
	}
	// 匹配 "https://raw.github.com"开头的链接
	if strings.HasPrefix(rawPath, "https://raw.github.com") {
		return true, nil
	}
	// 匹配 "https://gist.githubusercontent.com"开头的链接
	if strings.HasPrefix(rawPath, "https://gist.githubusercontent.com") {
		return true, nil
	}
	// 匹配 "https://gist.github.com"开头的链接
	if strings.HasPrefix(rawPath, "https://gist.github.com") {
		return true, nil
	}
	if cfg.Shell.RewriteAPI {
		// 匹配 "https://api.github.com/"开头的链接
		if strings.HasPrefix(rawPath, "https://api.github.com") {
			return true, nil
		}
	}
	return false, nil
}

// 匹配文件扩展名是sh的rawPath
func MatcherShell(rawPath string) bool {
	return strings.HasSuffix(rawPath, ".sh")
}

// LinkProcessor 是一个函数类型，用于处理提取到的链接。
type LinkProcessor func(string) string

// 自定义 URL 修改函数
func modifyURL(url string, host string, cfg *config.Config) string {
	// 去除url内的https://或http://
	matched, err := EditorMatcher(url, cfg)
	if err != nil {
		logDump("Invalid URL: %s", url)
		return url
	}
	if matched {
		var u = url
		u = strings.TrimPrefix(u, "https://")
		u = strings.TrimPrefix(u, "http://")
		logDump("Modified URL: %s", "https://"+host+"/"+u)
		return "https://" + host + "/" + u
	}
	return url
}

var (
	matchedMatchers = []string{
		"blob",
		"raw",
		"gist",
	}
)

// matchString 检查目标字符串是否在给定的字符串集合中
func matchString(target string, stringsToMatch []string) bool {
	matchMap := make(map[string]struct{}, len(stringsToMatch))
	for _, str := range stringsToMatch {
		matchMap[str] = struct{}{}
	}
	_, exists := matchMap[target]
	return exists
}

// extractParts 从给定的 URL 中提取所需的部分
func extractParts(rawURL string) (string, string, string, url.Values, error) {
	// 解析 URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", nil, err
	}

	// 获取路径部分并分割
	pathParts := strings.Split(parsedURL.Path, "/")

	// 提取所需的部分
	if len(pathParts) < 3 {
		return "", "", "", nil, fmt.Errorf("URL path is too short")
	}

	// 提取 /WJQSERVER-STUDIO 和 /go-utils.git
	repoOwner := "/" + pathParts[1]
	repoName := "/" + pathParts[2]

	// 剩余部分
	remainingPath := strings.Join(pathParts[3:], "/")
	if remainingPath != "" {
		remainingPath = "/" + remainingPath
	}

	// 查询参数
	queryParams := parsedURL.Query()

	return repoOwner, repoName, remainingPath, queryParams, nil
}

var urlPattern = regexp.MustCompile(`https?://[^\s'"]+`)

// processLinks 处理链接，返回包含处理后数据的 io.Reader
func processLinks(input io.ReadCloser, compress string, host string, cfg *config.Config) (readerOut io.Reader, written int64, err error) {
	pipeReader, pipeWriter := io.Pipe() // 创建 io.Pipe
	readerOut = pipeReader

	go func() { // 在 Goroutine 中执行写入操作
		defer func() {
			if pipeWriter != nil { // 确保 pipeWriter 关闭，即使发生错误
				if err != nil {
					if closeErr := pipeWriter.CloseWithError(err); closeErr != nil { // 如果有错误，传递错误给 reader
						logError("pipeWriter close with error failed: %v, original error: %v", closeErr, err)
					}
				} else {
					if closeErr := pipeWriter.Close(); closeErr != nil { // 没有错误，正常关闭
						logError("pipeWriter close failed: %v", closeErr)
						if err == nil { // 如果之前没有错误，记录关闭错误
							err = closeErr
						}
					}
				}
			}
		}()

		defer func() {
			if err := input.Close(); err != nil {
				logError("input close failed: %v", err)
			}

		}()

		var bufReader *bufio.Reader

		if compress == "gzip" {
			// 解压gzip
			gzipReader, gzipErr := gzip.NewReader(input)
			if gzipErr != nil {
				err = fmt.Errorf("gzip解压错误: %v", gzipErr)
				return // Goroutine 中使用 return 返回错误
			}
			defer gzipReader.Close()
			bufReader = bufio.NewReader(gzipReader)
		} else {
			bufReader = bufio.NewReader(input)
		}

		var bufWriter *bufio.Writer
		var gzipWriter *gzip.Writer

		// 根据是否gzip确定 writer 的创建
		if compress == "gzip" {
			gzipWriter = gzip.NewWriter(pipeWriter)           // 使用 pipeWriter
			bufWriter = bufio.NewWriterSize(gzipWriter, 4096) //设置缓冲区大小
		} else {
			bufWriter = bufio.NewWriterSize(pipeWriter, 4096) // 使用 pipeWriter
		}

		//确保writer关闭
		defer func() {
			var closeErr error // 局部变量，用于保存defer中可能发生的错误

			if gzipWriter != nil {
				if closeErr = gzipWriter.Close(); closeErr != nil {
					logError("gzipWriter close failed %v", closeErr)
					// 如果已经存在错误，则保留。否则，记录此错误。
					if err == nil {
						err = closeErr
					}
				}
			}
			if flushErr := bufWriter.Flush(); flushErr != nil {
				logError("writer flush failed %v", flushErr)
				// 如果已经存在错误，则保留。否则，记录此错误。
				if err == nil {
					err = flushErr
				}
			}
		}()

		// 使用正则表达式匹配 http 和 https 链接
		for {
			line, readErr := bufReader.ReadString('\n')
			if readErr != nil {
				if readErr == io.EOF {
					break // 文件结束
				}
				err = fmt.Errorf("读取行错误: %v", readErr) // 传递错误
				return                                 // Goroutine 中使用 return 返回错误
			}

			// 替换所有匹配的 URL
			modifiedLine := urlPattern.ReplaceAllStringFunc(line, func(originalURL string) string {
				logDump("originalURL: %s", originalURL)
				return modifyURL(originalURL, host, cfg) // 假设 modifyURL 函数已定义
			})

			n, writeErr := bufWriter.WriteString(modifiedLine)
			written += int64(n) // 更新写入的字节数
			if writeErr != nil {
				err = fmt.Errorf("写入文件错误: %v", writeErr) // 传递错误
				return                                   // Goroutine 中使用 return 返回错误
			}
		}

		// 在返回之前，再刷新一次 (虽然 defer 中已经有 flush，但这里再加一次确保及时刷新)
		if flushErr := bufWriter.Flush(); flushErr != nil {
			if err == nil { // 避免覆盖之前的错误
				err = flushErr
			}
			return // Goroutine 中使用 return 返回错误
		}
	}()

	return readerOut, written, nil // 返回 reader 和 written，error 由 Goroutine 通过 pipeWriter.CloseWithError 传递
}
