#!/bin/bash

#NDK PATH
export ANDROID_NDK_HOME=$HOME/Android/Sdk/ndk/android-ndk-r25b/
if [ ! -d "$ANDROID_NDK_HOME" ]; then
    echo "Warning: Directory $ANDROID_NDK_HOME does not exist."
    exit 1
fi
#PKTD PATH
export PKTD_PATH=./
#Output lib files to path
export ANODIUM_LIBS=./bin/android
mkdir ./bin/android

echo "Building for android 32 bit 386 (emulator) -> jniLibs (v29)"
#Build for android 32 bit 386 (emulator) -> jniLibs (v29)
GOMOBILE="$HOME/go/pkg/gomobile" GOOS=android GOARCH=386 CC=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android29-clang CXX=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android29-clang++ CGO_ENABLED=1 GOARM=7 go build -p=8 -pkgdir=$GOMOBILE/pkg_android_arm -tags="" -ldflags="-s -w -extldflags=-pie" -o $ANODIUM_LIBS/x86/libpld.so -x $PKTD_PATH/lnd/cmd/lnd/main.go
echo "Building for android 64 bit 386 (emulator) -> jniLibs (v29)"
#Build for android 64 bit 386 (emulator) -> jniLibs (v29)
GOMOBILE="$HOME/go/pkg/gomobile" GOOS=android GOARCH=amd64 CC=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android29-clang CXX=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android29-clang++ CGO_ENABLED=1 GOARM=7 go build -p=8 -pkgdir=$GOMOBILE/pkg_android_arm -tags="" -ldflags="-s -w -extldflags=-pie" -o $ANODIUM_LIBS/x86_64/libpld.so -x $PKTD_PATH/lnd/cmd/lnd/main.go
echo "Building for android 32 bit ARM -> jniLibs (v29)"
#Build for android 64 bit ARM -> jniLibs (v29)
GOMOBILE="$HOME/go/pkg/gomobile" GOOS=android GOARCH=arm64 CC=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android29-clang CXX=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android29-clang++ CGO_ENABLED=1 GOARM=7 go build -p=8 -pkgdir=$GOMOBILE/pkg_android_arm -tags="" -ldflags="-s -w -extldflags=-pie" -o $ANODIUM_LIBS/arm64-v8a/libpld.so -x $PKTD_PATH/lnd/cmd/lnd/main.go
echo "Building for android 32 bit ARM -> jniLibs (v29)"
#Build for android 32 bit ARM -> jniLibs (v29)
GOMOBILE="$HOME/go/pkg/gomobile" GOOS=android GOARCH=arm CC=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi29-clang CXX=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi29-clang++ CGO_ENABLED=1 GOARM=7 go build -p=8 -pkgdir=$GOMOBILE/pkg_android_arm -tags="" -ldflags="-s -w -extldflags=-pie" -o $ANODIUM_LIBS/armeabi-v7a/libpld.so -x $PKTD_PATH/lnd/cmd/lnd/main.go