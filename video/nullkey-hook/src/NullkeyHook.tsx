import React, {useLayoutEffect, useRef} from 'react';
import {AbsoluteFill, Easing, Img, interpolate, staticFile, useCurrentFrame} from 'remotion';

const W = 1920;
const H = 1080;
const BLACK = '#0b0b0b';
const WHITE = '#f7f7f5';
const INK = '#111111';
const MUTED = '#666663';
const LINE = '#c8c8c3';
const RED = '#b4232d';
const RED_LIGHT = '#f8dfe1';

const clamp = (n: number) => Math.max(0, Math.min(1, n));
const ease = (n: number) => 1 - Math.pow(1 - clamp(n), 3);
const progress = (frame: number, start: number, duration: number) => clamp((frame - start) / duration);

const text = (
  ctx: CanvasRenderingContext2D,
  value: string,
  x: number,
  y: number,
  size: number,
  color = INK,
  weight = 500,
  align: CanvasTextAlign = 'left',
  mono = false,
) => {
  ctx.save();
  ctx.fillStyle = color;
  ctx.font = `${weight} ${size}px ${mono ? 'SFMono-Regular, Menlo, monospace' : '-apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif'}`;
  ctx.textAlign = align;
  ctx.textBaseline = 'middle';
  ctx.fillText(value, x, y);
  ctx.restore();
};

const rect = (
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  width: number,
  height: number,
  radius: number,
  fill: string,
  stroke = 'transparent',
  alpha = 1,
) => {
  ctx.save();
  ctx.globalAlpha = alpha;
  ctx.beginPath();
  ctx.roundRect(x, y, width, height, radius);
  ctx.fillStyle = fill;
  ctx.fill();
  ctx.strokeStyle = stroke;
  ctx.lineWidth = 2;
  ctx.stroke();
  ctx.restore();
};

const line = (
  ctx: CanvasRenderingContext2D,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  color: string,
  width = 2,
  alpha = 1,
) => {
  ctx.save();
  ctx.globalAlpha = alpha;
  ctx.strokeStyle = color;
  ctx.lineWidth = width;
  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x2, y2);
  ctx.stroke();
  ctx.restore();
};

const arrow = (ctx: CanvasRenderingContext2D, x: number, y: number, color: string, alpha = 1) => {
  line(ctx, x - 32, y, x + 20, y, color, 3, alpha);
  line(ctx, x + 20, y, x + 5, y - 13, color, 3, alpha);
  line(ctx, x + 20, y, x + 5, y + 13, color, 3, alpha);
};

const drawLogoSafeArea = (ctx: CanvasRenderingContext2D) => {
  // The actual Nullkey mark is rendered by the React Img layer.
  ctx.save();
  ctx.restore();
};

const background = (ctx: CanvasRenderingContext2D, color: string) => {
  ctx.fillStyle = color;
  ctx.fillRect(0, 0, W, H);
};

const terminal = (ctx: CanvasRenderingContext2D, x: number, y: number, alpha: number) => {
  rect(ctx, x, y, 650, 330, 14, WHITE, LINE, alpha);
  text(ctx, '●  ●  ●', x + 26, y + 30, 17, RED, 700, 'left');
  text(ctx, 'Agent Terminal', x + 132, y + 30, 18, MUTED, 600);
  line(ctx, x, y + 60, x + 650, y + 60, LINE, 1, alpha);
  text(ctx, '› 正在准备模型请求', x + 38, y + 112, 23, INK, 600);
  text(ctx, 'POST /v1/responses', x + 38, y + 160, 20, INK, 600, 'left', true);
  text(ctx, 'Authorization:  ?', x + 38, y + 207, 20, MUTED, 500, 'left', true);
  line(ctx, x + 38, y + 272, x + 170, y + 272, INK, 4, alpha);
};

const fileCard = (
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  title: string,
  detail: string,
  code: string,
  alpha: number,
  danger = false,
) => {
  rect(ctx, x, y, 500, 118, 10, danger ? RED_LIGHT : WHITE, danger ? RED : LINE, alpha);
  rect(ctx, x + 20, y + 21, 54, 54, 8, danger ? RED : WHITE, danger ? RED : INK, alpha);
  text(ctx, code, x + 47, y + 48, 15, danger ? WHITE : INK, 700, 'center');
  text(ctx, title, x + 96, y + 43, 23, INK, 650);
  text(ctx, detail, x + 96, y + 78, 16, MUTED, 500);
};

const movingDot = (
  ctx: CanvasRenderingContext2D,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  amount: number,
  color: string,
) => {
  const t = ease(amount);
  const x = x1 + (x2 - x1) * t;
  const y = y1 + (y2 - y1) * t;
  ctx.beginPath();
  ctx.arc(x, y, 7, 0, Math.PI * 2);
  ctx.fillStyle = color;
  ctx.fill();
};

const sceneHook = (ctx: CanvasRenderingContext2D, frame: number) => {
  background(ctx, WHITE);
  if (frame < 42) {
    const a = ease(progress(frame, 0, 10));
    ctx.save();
    ctx.globalAlpha = a;
    text(ctx, '让 Agent 工作，不把钥匙交给它。', W / 2, 820, 28, INK, 600, 'center');
    ctx.save();
    ctx.globalAlpha = a * 0.7;
    line(ctx, 780, 700, 1140, 700, INK, 3, a);
    ctx.restore();
    ctx.restore();
  } else if (frame < 88) {
    const a = ease(progress(frame, 42, 12));
    text(ctx, '如果 Agent 能使用', W / 2, 390, 70, INK, 700, 'center');
    text(ctx, 'API key，却看不到它？', W / 2, 485, 82, INK, 700, 'center');
    text(ctx, '一个关于信任边界的问题。', W / 2, 720, 28, MUTED, 500, 'center');
    line(ctx, 600, 575, 1320, 575, RED, 3, a);
  } else {
    const a = ease(progress(frame, 88, 18));
    terminal(ctx, 220, 455, a);
    rect(ctx, 1260, 505, 410, 150, 12, WHITE, LINE, a);
    text(ctx, 'API', 1300, 555, 17, INK, 700, 'left', true);
    text(ctx, '模型 API', 1380, 555, 23, INK, 650);
    text(ctx, '等待认证', 1380, 600, 16, MUTED, 500);
    line(ctx, 880, 620, 1232, 620, LINE, 3, a);
    arrow(ctx, 1210, 620, RED, a);
    text(ctx, '能使用，但看不到。', W / 2, 900, 39, MUTED, 600, 'center');
  }
  drawLogoSafeArea(ctx);
};

const sceneSpread = (ctx: CanvasRenderingContext2D, frame: number) => {
  background(ctx, WHITE);
  const terminalAlpha = ease(progress(frame, 0, 20));
  const entrance = ease(progress(frame, 0, 28));
  terminal(ctx, 120 - (1 - entrance) * 180, 260, terminalAlpha);
  text(ctx, '真实凭证进入运行环境，', 960, 870, 45, INK, 700, 'center');
  text(ctx, '随后被写入更多会话材料。', 960, 925, 32, MUTED, 500, 'center');

  const files = [
    ['环境变量', 'env / process', 'ENV'],
    ['配置文件', 'config.toml', 'CFG'],
    ['会话记录', 'session.jsonl', 'LOG'],
  ];
  files.forEach(([title, detail, code], index) => {
    const start = 28 + index * 28;
    const alpha = ease(progress(frame, start, 24));
    const y = 190 + index * 180;
    fileCard(ctx, 1180 + (1 - entrance) * 180, y, title, detail, code, alpha);
    line(ctx, 785, 420 + index * 63, 1140 + (1 - entrance) * 180, y + 58, LINE, 2, alpha * entrance);
    const dotProgress = progress(frame, start + 20, 55);
    if (dotProgress > 0 && dotProgress < 1) {
      movingDot(ctx, 785, 420 + index * 63, 1140 + (1 - entrance) * 180, y + 58, dotProgress, INK);
    }
  });
};

const sceneRisk = (ctx: CanvasRenderingContext2D, frame: number) => {
  const finalTransition = frame >= 240;
  background(ctx, finalTransition ? BLACK : WHITE);
  const foreground = finalTransition ? WHITE : INK;
  const muted = finalTransition ? '#aaa' : MUTED;
  const danger = finalTransition ? '#ff7882' : RED;
  if (finalTransition) {
    text(ctx, '会话泄漏，不应变成凭证泄漏。', 960, 475, 62, danger, 700, 'center');
    text(ctx, '一次普通的分享，可能带走长期凭证。', 960, 545, 25, '#c7c7c7', 500, 'center');
    return;
  }
  const alpha = ease(progress(frame, 0, 22));
  const entrance = ease(progress(frame, 0, 28));

  const left = [
    ['会话记录', 'session.jsonl', 'SES'],
    ['运行日志', 'agent.log', 'LOG'],
    ['命令历史', '.zsh_history', 'HIS'],
  ];
  left.forEach(([title, detail, code], index) => {
    fileCard(ctx, 105 - (1 - entrance) * 180, 250 + index * 155, title, detail, code, alpha);
    line(ctx, 610, 309 + index * 155, 775, 485, danger, 3, alpha);
    arrow(ctx, 760, 485, danger, alpha);
  });

  rect(ctx, 780, 385, 280, 200, 12, finalTransition ? '#341b1e' : RED_LIGHT, danger, alpha);
  text(ctx, 'ZIP', 920, 455, 35, danger, 700, 'center');
  text(ctx, '工作区归档', 920, 515, 19, danger, 600, 'center');
  rect(ctx, 845, 625, 150, 42, 7, finalTransition ? '#341b1e' : RED_LIGHT, danger, alpha);
  text(ctx, 'sk-test-••••', 920, 646, 19, danger, 500, 'center', true);

  const exits = [
    ['分享给同事', '聊天与协作', '发'],
    ['提交问题', '日志与附件', '单'],
    ['云端备份', '归档与同步', '云'],
  ];
  exits.forEach(([title, detail, code], index) => {
    const y = 230 + index * 170;
    fileCard(ctx, 1290 + (1 - entrance) * 180, y, title, detail, code, alpha, true);
    line(ctx, 1060, 485, 1265, y + 59, danger, 3, alpha);
    arrow(ctx, 1245, y + 59, danger, alpha);
  });

  text(ctx, '材料离开工作区，凭证也跟着离开。', 960, 900, 38, INK, 650, 'center');
  void muted;
  void foreground;
};

const architectureNode = (
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  width: number,
  title: string,
  detail: string,
  alpha: number,
  danger = false,
) => {
  rect(ctx, x, y, width, 150, 14, WHITE, danger ? RED : LINE, alpha);
  text(ctx, title, x + 32, y + 54, 25, danger ? RED : INK, 700);
  text(ctx, detail, x + 32, y + 98, 17, MUTED, 500);
};

const sceneSolution = (ctx: CanvasRenderingContext2D, frame: number) => {
  background(ctx, WHITE);
  const transition = frame < 24 ? 1 - ease(progress(frame, 0, 24)) : 0;
  if (transition > 0) {
    ctx.fillStyle = `rgba(11,11,11,${transition})`;
    ctx.fillRect(0, 0, W, H);
  }

  const hostAlpha = ease(progress(frame, 10, 28));
  const gatewayAlpha = ease(progress(frame, 62, 28));
  const sandboxAlpha = ease(progress(frame, 105, 28));
  const linesAlpha = ease(progress(frame, 145, 32));
  const labelsAlpha = ease(progress(frame, 220, 30));
  const servicesAlpha = ease(progress(frame, 275, 34));
  const statusAlpha = ease(progress(frame, 325, 24));

  text(ctx, '真实凭证留在宿主机。', 960, 170, 54, INK, 700, 'center', false);
  text(ctx, 'Agent 只获得完成任务所需的能力。', 960, 225, 25, MUTED, 500, 'center');

  architectureNode(ctx, 155, 415, 430, 'macOS Keychain', '真实 API key · OAuth token', hostAlpha);
  architectureNode(ctx, 745, 415, 430, 'Nullkey Gateway', '宿主侧认证与流式转发', gatewayAlpha);
  architectureNode(ctx, 1335, 415, 430, 'Agent Sandbox', '只有 per-sandbox token', sandboxAlpha);

  line(ctx, 585, 490, 735, 490, LINE, 3, linesAlpha);
  arrow(ctx, 710, 490, INK, linesAlpha);
  line(ctx, 1175, 490, 1325, 490, LINE, 3, linesAlpha);
  arrow(ctx, 1300, 490, INK, linesAlpha);

  const tokenProgress = progress(frame, 165, 100);
  if (tokenProgress > 0 && tokenProgress < 1) {
    movingDot(ctx, 1325, 490, 735, 490, tokenProgress, INK);
  }
  const requestProgress = progress(frame, 275, 100);
  if (requestProgress > 0 && requestProgress < 1) {
    movingDot(ctx, 735, 520, 1325, 520, requestProgress, RED);
  }

  if (labelsAlpha > 0) {
    text(ctx, '真实凭证只在 Gateway 之外被使用', 960, 720, 30, INK, 650, 'center');
    text(ctx, '沙箱请求携带临时 token，停止后立即失效。', 960, 770, 22, MUTED, 500, 'center');
    rect(ctx, 660, 850, 600, 70, 10, RED_LIGHT, RED, labelsAlpha);
    text(ctx, '真实凭证不进入沙箱。', 960, 885, 28, RED, 700, 'center');
  }

  if (servicesAlpha > 0) {
    const services = [
      ['模型服务', 'MODEL'],
      ['MCP 连接', 'MCP'],
      ['私有 Git', 'GIT'],
    ];
    services.forEach(([name, code], index) => {
      const x = 1215 + index * 205;
      const y = 690 + Math.sin((frame - 275) / 14 + index) * 5;
      rect(ctx, x, y, 175, 72, 9, WHITE, LINE, servicesAlpha);
      text(ctx, code, x + 18, y + 25, 14, INK, 700, 'left', true);
      text(ctx, name, x + 18, y + 51, 16, MUTED, 600);
      line(ctx, 1540, 565, x + 82, y, LINE, 2, servicesAlpha * 0.8);
    });
    const serviceFlow = progress(frame, 300, 50);
    if (serviceFlow > 0 && serviceFlow < 1) {
      movingDot(ctx, 1370, 565, 1295, 690, serviceFlow, RED);
    }
  }

  if (statusAlpha > 0) {
    rect(ctx, 650, 965, 620, 54, 9, statusAlpha > 0.75 ? RED_LIGHT : WHITE, statusAlpha > 0.75 ? RED : LINE, statusAlpha);
    text(ctx, statusAlpha > 0.75 ? '停止沙箱 → token 立即失效' : 'Gateway 已验证临时 token', 960, 992, 21, statusAlpha > 0.75 ? RED : INK, 650, 'center');
  }
};

const sceneDemo = (ctx: CanvasRenderingContext2D, frame: number) => {
  background(ctx, WHITE);
  const fade = frame < 20 ? ease(progress(frame, 0, 20)) : 1;
  ctx.save();
  ctx.globalAlpha = fade;
  text(ctx, '真实演示：gateway 注入凭证', 120, 120, 48, INK, 700);
  text(ctx, 'GUI 启动 Codex，宿主 gateway 才接触真实凭证。', 120, 170, 23, MUTED, 500);

  const guiIn = ease(progress(frame, 18, 28));
  rect(ctx, 120, 245, 760, 560, 18, '#f4f4f1', LINE, guiIn);
  rect(ctx, 120, 245, 760, 62, 18, '#e9e9e5', LINE, guiIn);
  text(ctx, 'Nullkey', 158, 278, 22, INK, 700);
  text(ctx, 'API 服务', 355, 278, 18, MUTED, 600);
  text(ctx, '连接 Codex', 520, 278, 18, MUTED, 600);
  rect(ctx, 165, 350, 670, 90, 12, WHITE, LINE, guiIn);
  text(ctx, '凭证', 198, 382, 16, MUTED, 600);
  text(ctx, 'Demo ChatGPT', 198, 416, 25, INK, 650);
  rect(ctx, 165, 470, 320, 88, 12, WHITE, LINE, guiIn);
  text(ctx, '工作区', 198, 502, 16, MUTED, 600);
  text(ctx, 'demo', 198, 536, 25, INK, 650);
  rect(ctx, 515, 470, 320, 88, 12, WHITE, LINE, guiIn);
  text(ctx, '网络模式', 548, 502, 16, MUTED, 600);
  text(ctx, 'Filtered', 548, 536, 25, INK, 650);
  const buttonProgress = ease(progress(frame, 70, 24));
  rect(ctx, 165, 620, 670, 92, 12, buttonProgress > 0.7 ? INK : '#deded9', buttonProgress > 0.7 ? INK : LINE, guiIn);
  text(ctx, '启动 Codex', 500, 666, 27, buttonProgress > 0.7 ? WHITE : MUTED, 700, 'center');
  if (buttonProgress > 0.7) {
    rect(ctx, 165, 746, 670, 30, 6, RED_LIGHT, RED, 1);
    text(ctx, '终端已就绪 · sandbox/demo', 500, 761, 16, RED, 650, 'center');
  }

  const termIn = ease(progress(frame, 115, 26));
  rect(ctx, 980, 245, 820, 560, 18, '#111111', '#333333', termIn);
  text(ctx, 'Codex · sandbox/demo', 1020, 282, 20, '#d7d7d2', 600, 'left', true);
  line(ctx, 980, 315, 1800, 315, '#333333', 1, termIn);
  text(ctx, '$ env | grep NULLKEY', 1020, 365, 21, '#f7f7f5', 600, 'left', true);
  text(ctx, 'NULLKEY_TOKEN=sbx_demo_••••', 1020, 404, 21, '#9ee493', 600, 'left', true);
  text(ctx, '$ hello', 1020, 475, 21, '#f7f7f5', 600, 'left', true);
  const response = ease(progress(frame, 185, 26));
  if (response > 0) {
    text(ctx, 'pong from fake upstream', 1020, 535, 30, '#f7f7f5', 700, 'left', true);
    rect(ctx, 1015, 585, 430, 54, 8, '#203521', '#5d9b68', response);
    text(ctx, 'model response received', 1036, 612, 17, '#9ee493', 600, 'left', true);
  }

  const logIn = ease(progress(frame, 225, 25));
  rect(ctx, 120, 870, 1680, 104, 12, '#f4f4f1', LINE, logIn);
  text(ctx, 'fake upstream · 127.0.0.1:49960', 155, 910, 18, MUTED, 600, 'left', true);
  text(ctx, 'POST /backend-api/codex/responses', 610, 910, 18, INK, 600, 'left', true);
  text(ctx, 'Authorization: Bearer fake-oauth-token', 1120, 910, 18, RED, 700, 'left', true);
  text(ctx, 'gateway 注入发生在沙箱之外', 960, 1008, 24, RED, 700, 'center');
  ctx.restore();
};

const draw = (ctx: CanvasRenderingContext2D, frame: number) => {
  if (frame < 180) sceneHook(ctx, frame);
  else if (frame < 420) sceneSpread(ctx, frame - 180);
  else if (frame < 690) sceneRisk(ctx, frame - 420);
  // Preserve the existing first 32 seconds; the documented GUI demo starts here.
  else if (frame < 960) sceneSolution(ctx, frame - 690);
  else sceneDemo(ctx, frame - 960);
};

export const NullkeyHook: React.FC = () => {
  const frame = useCurrentFrame();
  const canvas = useRef<HTMLCanvasElement>(null);
  const demoFrame = frame - 960;
  const logoOpacity = interpolate(frame, [0, 12, 42, 62], [0, 1, 1, 1], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const logoScale = interpolate(frame, [0, 18, 42], [0.72, 1, 1], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const logoTranslateX = interpolate(frame, [30, 62], [-52, -888], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const logoTranslateY = interpolate(frame, [30, 62], [-52, -354], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const logoSize = interpolate(frame, [30, 62], [104, 48], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const brandOpacity = interpolate(frame, [10, 22, 42], [0, 1, 0], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  const brandTranslateY = interpolate(frame, [10, 22], [24, 0], {
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
    easing: Easing.bezier(0.16, 1, 0.3, 1),
  });
  useLayoutEffect(() => {
    const context = canvas.current?.getContext('2d');
    if (context) draw(context, frame);
  }, [frame]);
  return (
    <AbsoluteFill style={{background: BLACK}}>
      <canvas ref={canvas} width={W} height={H} style={{width: '100%', height: '100%'}} />
      {demoFrame >= 0 && demoFrame < 180 ? (
        <Img
          src={staticFile('nullkey-window-crop.png')}
          style={{
            position: 'absolute',
            left: 108,
            top: 240,
            width: 820,
            height: 413,
            objectFit: 'cover',
            objectPosition: 'center',
            borderRadius: 16,
            opacity: interpolate(demoFrame, [0, 18, 150, 180], [0, 1, 1, 0], {
              extrapolateLeft: 'clamp',
              extrapolateRight: 'clamp',
            }),
            boxShadow: '0 18px 50px rgba(0,0,0,0.14)',
          }}
        />
      ) : null}
      <div
        style={{
          position: 'absolute',
          left: '50%',
          top: 402,
          width: logoSize,
          height: logoSize,
          opacity: logoOpacity,
          scale: logoScale,
          translate: `${logoTranslateX}px ${logoTranslateY}px`,
        }}
      >
        <Img src={staticFile('nullkey-app-icon.png')} style={{width: '100%', height: '100%'}} />
      </div>
      <div
        style={{
          position: 'absolute',
          left: '50%',
          top: 525,
          width: 800,
          translate: `-400px ${brandTranslateY}px`,
          opacity: brandOpacity,
          textAlign: 'center',
          color: INK,
          fontFamily: '-apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif',
        }}
      >
        <div style={{fontSize: 72, fontWeight: 700, lineHeight: 1.05}}>Nullkey</div>
        <div style={{fontSize: 24, fontWeight: 500, color: MUTED, marginTop: 12}}>本地 Agent 安全沙箱</div>
      </div>
    </AbsoluteFill>
  );
};
