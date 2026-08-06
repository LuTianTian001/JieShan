import type { Protocol, V2EndpointCapabilities } from './types';

export function normalizeInferenceProtocol(value: string | undefined): Protocol | null {
  const normalized = value?.trim().toLowerCase();
  if (normalized === 'openai') return 'openai';
  if (normalized === 'anthropic') return 'anthropic';
  if (normalized === 'gemini') return 'gemini';
  if (normalized === 'compatible' || normalized === 'openai-compatible' || normalized === 'openai_compatible'
    || normalized === 'openai_chat_completions' || normalized === 'openai_responses') return 'compatible';
  return null;
}

export function inferenceProtocolCapabilities(value: string | undefined): V2EndpointCapabilities {
  const protocol = normalizeInferenceProtocol(value);
  if (protocol === 'openai' || protocol === 'compatible') {
    return { modelDiscovery: true, chatCompletions: true, responses: true, routeEligible: true };
  }
  if (protocol === 'anthropic' || protocol === 'gemini') {
    return { modelDiscovery: true, chatCompletions: false, responses: false, routeEligible: false };
  }
  return { modelDiscovery: false, chatCompletions: false, responses: false, routeEligible: false };
}

export function inferenceProtocolLabel(value: string | undefined): string {
  const protocol = normalizeInferenceProtocol(value);
  if (protocol === 'openai') return 'OpenAI 官方';
  if (protocol === 'compatible') return 'OpenAI 兼容';
  if (protocol === 'anthropic') return 'Anthropic 原生';
  if (protocol === 'gemini') return 'Gemini 原生';
  if (value === 'mixed') return '混合协议';
  return value || '未知协议';
}

export function inferenceProtocolHint(value: string | undefined): string {
  const protocol = normalizeInferenceProtocol(value);
  if (protocol === 'openai') return '支持模型获取、Chat Completions 和 Responses 路由。';
  if (protocol === 'compatible') return '支持 OpenAI 路由；Responses 是否可用仍取决于该中转站的实现。';
  if (protocol === 'anthropic' || protocol === 'gemini') return '当前仅用于获取模型列表，不能加入 OpenAI 下游路由。';
  return '该协议不受支持。';
}

export function inferenceProtocolAuthScheme(value: string | undefined): string {
  const protocol = normalizeInferenceProtocol(value);
  if (protocol === 'anthropic') return 'x-api-key';
  if (protocol === 'gemini') return 'query-key';
  return 'bearer';
}
