cmake_minimum_required(VERSION 3.5.0)
project(ohos_entry)

add_library(entry SHARED napi_init.cpp)

# NAPI 运行时
target_link_libraries(entry PUBLIC libace_napi.z.so)

# Go 引擎动态库由 scripts/shell/build-ohos.sh 编译到 entry/libs/arm64-v8a/
add_library(caotun SHARED IMPORTED)
set_target_properties(caotun PROPERTIES
  IMPORTED_LOCATION ${CMAKE_SOURCE_DIR}/../../../libs/${OHOS_ARCH}/libcaotun.so)
target_link_libraries(entry PUBLIC caotun)
