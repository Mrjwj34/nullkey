import React, {useLayoutEffect, useRef} from 'react';
import {AbsoluteFill, Img, staticFile, useCurrentFrame} from 'remotion';

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
  const a = ease(progress(frame, 0, 24));
  text(ctx, '如果 Agent 能使用', W / 2, 320, 72, INK, 700, 'center');
  text(ctx, 'API key，却看不到它？', W / 2, 408, 84, INK, 700, 'center');
  text(ctx, '能使用，但看不到。', W / 2, 900, 39, MUTED, 600, 'center');
  terminal(ctx, 220, 535, a);
  rect(ctx, 1260, 585, 410, 150, 12, WHITE, LINE, a);
  text(ctx, 'API', 1300, 635, 17, INK, 700, 'left', true);
  text(ctx, '模型 API', 1380, 635, 23, INK, 650);
  text(ctx, '等待认证', 1380, 680, 16, MUTED, 500);
  line(ctx, 880, 700, 1232, 700, LINE, 3, a);
  arrow(ctx, 1210, 700, RED, a);
  drawLogoSafeArea(ctx);
};

const sceneSpread = (ctx: CanvasRenderingContext2D, frame: number) => {
  background(ctx, WHITE);
  const terminalAlpha = ease(progress(frame, 0, 20));
  terminal(ctx, 120, 260, terminalAlpha);
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
    fileCard(ctx, 1180, y, title, detail, code, alpha);
    line(ctx, 785, 420 + index * 63, 1140, y + 58, LINE, 2, alpha);
    const dotProgress = progress(frame, start + 20, 55);
    if (dotProgress > 0 && dotProgress < 1) {
      movingDot(ctx, 785, 420 + index * 63, 1140, y + 58, dotProgress, INK);
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

  const left = [
    ['会话记录', 'session.jsonl', 'SES'],
    ['运行日志', 'agent.log', 'LOG'],
    ['命令历史', '.zsh_history', 'HIS'],
  ];
  left.forEach(([title, detail, code], index) => {
    fileCard(ctx, 105, 250 + index * 155, title, detail, code, alpha);
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
    fileCard(ctx, 1290, y, title, detail, code, alpha, true);
    line(ctx, 1060, 485, 1265, y + 59, danger, 3, alpha);
    arrow(ctx, 1245, y + 59, danger, alpha);
  });

  text(ctx, '材料离开工作区，凭证也跟着离开。', 960, 900, 38, INK, 650, 'center');
  void muted;
  void foreground;
};

const draw = (ctx: CanvasRenderingContext2D, frame: number) => {
  if (frame < 180) sceneHook(ctx, frame);
  else if (frame < 420) sceneSpread(ctx, frame - 180);
  else sceneRisk(ctx, frame - 420);
};

export const NullkeyHook: React.FC = () => {
  const frame = useCurrentFrame();
  const canvas = useRef<HTMLCanvasElement>(null);
  useLayoutEffect(() => {
    const context = canvas.current?.getContext('2d');
    if (context) draw(context, frame);
  }, [frame]);
  return (
    <AbsoluteFill style={{background: BLACK}}>
      <canvas ref={canvas} width={W} height={H} style={{width: '100%', height: '100%'}} />
      <Img src={staticFile('nullkey-cat.svg')} style={{position: 'absolute', left: 72, top: 48, width: 48, height: 48}} />
    </AbsoluteFill>
  );
};
