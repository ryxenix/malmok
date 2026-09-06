# 빠른 시작

SSH로 접근 가능한 기존 머신에 단일 서버 RKE2 클러스터를 구축합니다.
말목은 머신을 생성하거나 방화벽 정책을 바꾸지 않습니다.

## 준비 사항

- 운영자 머신: Linux 또는 macOS, amd64 또는 arm64
- 검증된 대상 OS: Ubuntu 22.04/24.04 또는 Rocky 9
- 대상 노드의 SSH 접근 및 root 권한 또는 정상적인 권한 상승 수단
- 최소 20GB 여유 디스크. CPU 2코어, RAM 4GB, 여유 디스크 50GB 권장

노드 역할에 따라 6443, 9345, 2379–2380, 10250과 데이터플레인 통신이 필요합니다.
정확한 도달 여부는 preflight가 검사합니다.

## 1. 클러스터 정의

[홈의 예제](../index.md#cluster-document)를 `cluster.yaml`로 저장하십시오.
`192.0.2.0/24`는 문서용 주소입니다. 실제 IP, SSH 계정, 키 경로와 RKE2 버전으로 바꿉니다.

!!! warning "확장 전에 등록 주소 확인"

    예제는 단일 서버의 주소를 등록 엔드포인트로 사용합니다. 컨트롤 플레인 서버를
    추가하기 전에 안정적인 DNS 이름이나 VIP를 마련하십시오. 등록 주소가 바뀌면
    기존 노드를 다시 조인해야 할 수 있습니다.

## 2. 문서 검증

```bash
malmok plan -f cluster.yaml --validate-only
```

SSH 접속 전에 알 수 없는 필드와 잘못된 조합을 검사합니다.

## 3. 노드 실측

```bash
malmok preflight -f cluster.yaml
```

호스트와 통신 경로를 확인하고 차단 사유와 권장 사항을 보고합니다. 노드를 수정하지 않습니다.

## 4. 계획 검토

```bash
malmok plan -f cluster.yaml
```

프로파일이 채운 값과 출처를 포함해 확정된 설정을 검토합니다.

## 5. 구축과 인계

```bash
malmok apply -f cluster.yaml
malmok report
```

실행 상태와 JSONL 이벤트를 기록합니다. 중단됐다면 출력된 실행 ID로 재개합니다.

```bash
# RUN_ID를 실제 실행 ID로 교체
malmok apply --resume RUN_ID
```

## 한국어 TUI 사용

```bash
malmok apply --tui --lang ko
```

TUI도 같은 문서와 엔진을 사용합니다. 노드에 접속하지 않고 화면을 체험하려면
`malmok apply --demo --lang ko`를 실행하십시오.
