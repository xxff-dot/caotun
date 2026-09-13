//go:build !openharmony

package main

// 非 openharmony 构建(本地编译检查/桌面)下无 hilog,降级为空实现
func hiLogf(string) {}
