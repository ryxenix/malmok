# 설치

말목은 Linux·macOS의 amd64·arm64용 정적 바이너리로 배포됩니다.
정확한 버전을 확인할 수 있도록 릴리스 바이너리 사용을 권장합니다.

## 최신 릴리스 설치

Bash와 Fish에서 같은 명령을 사용합니다.

```bash
curl -fsSL https://malmok.dev/install.sh | sh
```

운영체제와 아키텍처에 맞는 바이너리를 내려받아 `SHA256SUMS`와 대조합니다.
체크섬이 다르면 설치를 중단합니다. 기본 경로는 `/usr/local/bin`이며 쓰기 권한이
없을 때만 `sudo`를 사용합니다.

## 실행 전에 확인하기

```bash
curl -fsSLo install-malmok.sh https://malmok.dev/install.sh
less install-malmok.sh
sh install-malmok.sh
```

## 버전과 설치 경로 선택

```bash
# 특정 릴리스 설치
curl -fsSL https://malmok.dev/install.sh | sh -s -- --version v0.83.0

# 사용자 디렉터리에 설치
curl -fsSL https://malmok.dev/install.sh | sh -s -- --bin-dir ~/.local/bin

# Helm과 k9s를 포함하는 Linux 에어갭 바이너리 설치
curl -fsSL https://malmok.dev/install.sh | sh -s -- --airgap
```

!!! note "에어갭 바이너리와 클러스터 반입 자재"

    `--airgap`은 말목 바이너리의 종류를 선택합니다. 폐쇄망에 RKE2를 구축하려면
    별도의 노드 아티팩트, 이미지와 차트가 필요합니다.
    [에어갭 설치](../guides/air-gap.md)를 확인하십시오.

## 직접 내려받기

[릴리스 페이지](https://github.com/ryxenix/malmok/releases)에서 바이너리와
`SHA256SUMS`를 내려받아 체크섬을 확인한 뒤 설치하십시오. Linux amd64 예시입니다.

=== "Bash"

    ```bash
    # X.Y.Z를 내려받은 릴리스 버전으로 교체
    MALMOK_BIN=./malmok_vX.Y.Z_linux_amd64
    chmod +x "$MALMOK_BIN"
    sudo install "$MALMOK_BIN" /usr/local/bin/malmok
    malmok --version
    ```

=== "Fish"

    ```fish
    # X.Y.Z를 내려받은 릴리스 버전으로 교체
    set MALMOK_BIN ./malmok_vX.Y.Z_linux_amd64
    chmod +x "$MALMOK_BIN"
    sudo install "$MALMOK_BIN" /usr/local/bin/malmok
    malmok --version
    ```

## 소스에서 빌드

Go 1.25.8 이상이 필요합니다.

```bash
git clone https://github.com/ryxenix/malmok
cd malmok
go build -o bin/malmok ./cmd/malmok
```

`go install github.com/ryxenix/malmok/cmd/malmok@latest`도 사용할 수 있습니다.
소스 빌드는 버전이 `dev`로 표시되므로 정확한 릴리스 식별이 필요하면 배포 바이너리를 사용하십시오.
