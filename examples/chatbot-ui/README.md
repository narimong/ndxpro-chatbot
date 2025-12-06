# Eino Chatbot UI

Eino 챗봇 API 서버를 테스트하기 위한 웹 기반 UI입니다.

## 사전 요구사항

- Eino 챗봇 API 서버가 실행 중이어야 합니다
- OpenAI API 키가 필요합니다

## 사용 방법

### 1. API 서버 실행

```bash
cd examples/simple-chatbot
OPENAI_API_KEY=your-api-key go run ./api
```

서버가 `http://localhost:58080`에서 실행됩니다.

### 2. 테스트 UI 열기

브라우저에서 `index.html` 파일을 직접 열거나, 간단한 HTTP 서버를 실행합니다:

```bash
# Python 3
cd examples/chatbot-ui
python3 -m http.server 3000

# 또는 Node.js
npx serve .
```

브라우저에서 `http://localhost:3000` 접속

### 3. 사용하기

1. **서버 연결**: Server 입력란에 `localhost:58080` 확인 후 "연결" 클릭
2. **대화 시작**: 메시지를 입력하고 Enter 또는 "전송" 클릭
3. **스트리밍 응답**: 봇의 응답이 실시간으로 표시됩니다

## 기능

| 기능 | 설명 |
|------|------|
| 세션 관리 | 자동 세션 생성, 새 세션 생성 |
| 실시간 채팅 | WebSocket 기반 스트리밍 응답 |
| 히스토리 | 대화 내역 유지, 초기화 가능 |
| 시스템 프롬프트 | 커스텀 시스템 프롬프트 설정 |
| 자동 재연결 | 연결 끊김 시 자동 재연결 시도 |

## 테스트 시나리오

1. **기본 대화**
   - 서버 연결 → 메시지 전송 → 스트리밍 응답 확인

2. **히스토리 확인**
   - 여러 번 대화 → 이전 대화 맥락 유지 확인

3. **히스토리 초기화**
   - "초기화" 버튼 클릭 → 대화 내역 삭제 확인

4. **새 세션**
   - "새 세션" 버튼 클릭 → 새로운 세션 ID 확인

5. **시스템 프롬프트**
   - System Prompt 입력 후 새 세션 생성 → 동작 변경 확인

## 문제 해결

### 연결 실패
- API 서버가 실행 중인지 확인
- CORS 설정 확인 (API 서버에 이미 포함됨)
- 브라우저 개발자 도구에서 에러 확인

### 응답 없음
- OpenAI API 키가 올바른지 확인
- API 서버 로그 확인

## 파일 구조

```
chatbot-ui/
├── index.html    # HTML 구조
├── style.css     # 스타일시트 (다크모드 지원)
├── app.js        # JavaScript 로직
└── README.md     # 이 파일
```
