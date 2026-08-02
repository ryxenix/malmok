# platformctl

RKE2 기반 플랫폼 구축·운영 자동화 도구.
홈랩 / 회사 프로덕션 / 고객사(온라인·DMZ·폐쇄망) 전 케이스 단일 도구 대응.

**현재 상태: 설계 단계. 실행 코드 없음.**
구현은 Claude Code 로 진행한다. `CLAUDE.md` 를 먼저 읽을 것.

## 구조

```
CLAUDE.md                    세션 규약. 절대 금지 목록, 스택, 불변식, 문서 라우팅
docs/
├── 00-architecture.md       ADR 10건, 레이어 분해, Tier 제도, 로드맵
├── 10-preflight-plan.md     프로브 카탈로그, 강등 결정 트리
├── 11-execute.md            [미작성] phase / 멱등 / 재개 / 이벤트 스키마
├── 20-cert.md               인증서 라이프사이클 (조립·검증·갱신)
└── 30-maintenance.md        인증서 수명 관리, 정기 점검, 보고서
api/v1alpha1/                스키마 = 단일 원천. 주석이 명세다
├── types.go                 ClusterSpec
└── ingress.go               IngressSpec (gateway / listener / TLS / DNS)
examples/
└── cluster-dmz.yaml         DMZ 고객사 예시 (혼합 TLS 소스)
```

### 문서 분할 원칙

- **문서 1개 = 구현 세션 1개.** 한 문서를 읽고 구현·테스트·커밋이 닫혀야 한다
- **"왜"(ADR)와 "무엇"(WP 명세)을 분리한다.** 구현 세션은 `00` 을 읽지 않는다
- **개발 설계 문서와 납품 문서는 다른 물건이다.** 납품 문서(감사 리포트,
  유지보수 보고서)는 도구가 실행 결과로 생성한다. 설계 문서를 편집해 만들지 않는다
- **에러 코드 레지스트리는 문서로 만들지 않는다.** `internal/codes/` 가 원천,
  마크다운은 `go generate` 산출물

## 미작성 항목 (Claude Code 착수 대상)

| 항목 | 내용 |
|---|---|
| `docs/11-execute.md` | phase 정의, 멱등성 계약, 재개 모델, JSONL 이벤트 스키마 |
| `internal/codes/` | 흩어진 `PF-`/`PV-`/`MC-`/`DG-` 코드를 수집·정규화. **번호 충돌 확인 필요** |
| WP 문서 §7 수용 기준 | 기존 문서를 테스트 가능한 형태로 보강 |
| L0/L1 Ansible 롤 | |
| CI 매트릭스 (T1 6종) | libvirt VM, airgap 은 default route 제거로 실제 격리 |

`internal/codes/` 수집이 첫 작업으로 적합하다. 기존 3개 문서를 스캔해 코드를
추출·정규화하는 작업이고, 이 과정에서 번호 충돌(특히 `PF-8xx` 와 `PF-9xx` 경계)이
드러난다.

## 미결 사항

`docs/00-architecture.md` §8 참조. 사용자 결정 필요 항목은 임의로 정하지 않는다.
