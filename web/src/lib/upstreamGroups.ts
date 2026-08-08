import { protocolLabel, surfaceLabel } from './format';
import type { InferenceSurface, WireProtocol } from './types';

export interface GroupableUpstreamTarget {
  siteId: number;
  siteName: string;
  sourceModel: string;
  wireProtocol: WireProtocol;
  surface?: InferenceSurface;
  apiSurface?: InferenceSurface;
}

export interface UpstreamGroup<T extends GroupableUpstreamTarget> {
  key: string;
  siteId: number;
  siteName: string;
  sourceModel: string;
  targets: T[];
}

export function upstreamGroupKey(target: Pick<GroupableUpstreamTarget, 'siteId' | 'sourceModel'>): string {
  return `${target.siteId}:${target.sourceModel}`;
}

export function groupUpstreamTargets<T extends GroupableUpstreamTarget>(targets: readonly T[]): UpstreamGroup<T>[] {
  const groups: UpstreamGroup<T>[] = [];
  const groupsByKey = new Map<string, UpstreamGroup<T>>();

  targets.forEach((target) => {
    const key = upstreamGroupKey(target);
    const existing = groupsByKey.get(key);
    if (existing) {
      existing.targets.push(target);
      return;
    }
    const group = {
      key,
      siteId: target.siteId,
      siteName: target.siteName,
      sourceModel: target.sourceModel,
      targets: [target],
    };
    groupsByKey.set(key, group);
    groups.push(group);
  });

  return groups;
}

export function flattenUpstreamGroups<T extends GroupableUpstreamTarget, TValue>(
  groups: readonly UpstreamGroup<T>[],
  selectValue: (target: T) => TValue,
): TValue[] {
  return groups.flatMap((group) => group.targets.map(selectValue));
}

export function upstreamChannelSummary(targets: readonly GroupableUpstreamTarget[]): string {
  const surfacesByProtocol = new Map<WireProtocol, InferenceSurface[]>();

  targets.forEach((target) => {
    const surface = target.surface || target.apiSurface;
    const surfaces = surfacesByProtocol.get(target.wireProtocol) || [];
    if (surface && !surfaces.includes(surface)) surfaces.push(surface);
    if (!surfacesByProtocol.has(target.wireProtocol)) surfacesByProtocol.set(target.wireProtocol, surfaces);
  });

  return [...surfacesByProtocol.entries()].map(([protocol, surfaces]) => {
    if (!surfaces.length) return protocolLabel(protocol);
    return `${protocolLabel(protocol)} · ${surfaces.map(surfaceLabel).join(' / ')}`;
  }).join('；');
}
