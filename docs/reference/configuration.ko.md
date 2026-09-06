# 설정 레퍼런스

`cluster.yaml`은 원하는 상태를 정의하고 감사 근거로 남습니다.
알 수 없는 필드는 조용히 무시하지 않고 오류로 보고합니다.

```bash
malmok plan -f cluster.yaml --validate-only
```

## 문서 식별자

```yaml
apiVersion: malmok.dev/v1alpha1
kind: ClusterSpec
```

`v1alpha1` 스키마는 마이너 릴리스 사이에 변경될 수 있습니다.
업그레이드 전 [변경 이력](https://github.com/ryxenix/malmok/blob/main/CHANGELOG.md)을 확인하십시오.

## 프로파일

프로파일은 생략한 값을 채우는 출발점입니다. `malmok plan`에서 확정된 값과
그 출처를 확인하십시오. 프로파일 이름이 모든 구성의 하드웨어 검증을 뜻하지는 않습니다.

| 프로파일 | 용도 |
|---|---|
| `homelab` | 온라인 Ubuntu, Cilium Gateway API와 local-path 기본값 |
| `company-prod` | 온라인 사내 클러스터용 기본값 |
| `onprem-dmz` | 프록시 또는 허용 목록을 사용하는 온프레미스 |
| `airgap-ubuntu` | 에어갭 Ubuntu |
| `airgap-rocky` | 에어갭 Rocky |
| `airgap-conservative` | eBPF 데이터플레인을 사용하지 않는 에어갭 Rocky |
| `custom` | 프로파일 기본값 없이 구성, 계획 승인 시 명시적 확인 필요 |

## 최상위 항목

| 항목 | 역할 |
|---|---|
| `metadata` | 클러스터 이름, 프로파일과 현장 주석 |
| `network` | 망 모드, Pod·Service 네트워크와 라우팅 |
| `topology` | 등록 주소, VIP, 서버·에이전트와 SSH 접근 |
| `os` | OS 계열, NTP, swap과 보안 강화 사전 조건 |
| `kubernetes` | RKE2 버전, 아티팩트 경로, 데이터플레인과 etcd |
| `pki` | 인증서 공급원과 신뢰 배포 |
| `registry` | 이미지 레지스트리, 미러, 자격증명과 차트 공급원 |
| `storage` | 스토리지 관련 설정 |
| `gateway` | GatewayClass, 주소, 리스너, TLS와 DNS 리포트 |
| `platform` | GitOps와 관측 구성 요소 |
| `output` | 감사 리포트, 실행 번들과 이벤트 경로 |

## 시크릿 참조

자격증명을 평문으로 넣지 않고 환경변수나 파일을 참조합니다.

```yaml
registry:
  username: env://REGISTRY_USER
  password: file://./secrets/registry-password
```

문서에는 시크릿 값 대신 공급원을 기록합니다.

## 적용될 설정 확인

```bash
# 네트워크 접근 없이 문법과 스키마 검증
malmok plan -f cluster.yaml --validate-only

# 프로파일을 해석하고 실제 노드를 실측
malmok plan -f cluster.yaml
```

추가 예제는 [`examples/`](https://github.com/ryxenix/malmok/tree/main/examples)에 있습니다.
전체 필드 레퍼런스가 준비되기 전까지 스키마 기준은
[`api/v1alpha1`](https://github.com/ryxenix/malmok/tree/main/api/v1alpha1)의 Go 타입입니다.
