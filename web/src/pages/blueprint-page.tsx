import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type Edge,
  type EdgeChange,
  type NodeChange,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { LoaderCircle, RefreshCw, Workflow } from "lucide-react"

import { CanvasToolbox, GroupInspector, ListenerInspector } from "@/components/blueprint/inspector"
import { blueprintNodeTypes, type BlueprintNode } from "@/components/blueprint/nodes"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import type { ListenerRecord, ProxyGroup } from "@/lib/types"

interface BlueprintPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
}

type Selection = { kind: "listener" | "group"; id: string } | null

const LAYOUT_KEY = "hx-blueprint-layout-v1"
const COLUMN_WIDTH = 300
const ROW_HEIGHT = 150

function loadLayout(): Record<string, { x: number; y: number }> {
  try {
    return JSON.parse(window.localStorage.getItem(LAYOUT_KEY) ?? "{}")
  } catch {
    return {}
  }
}

function saveLayout(layout: Record<string, { x: number; y: number }>) {
  window.localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout))
}

// groupDepth returns the longest reference chain below each group so parents
// land in columns left of their children.
function groupDepths(groups: ProxyGroup[]): Map<string, number> {
  const byID = new Map(groups.map((group) => [group.id, group]))
  const depths = new Map<string, number>()
  const visiting = new Set<string>()
  function depth(id: string): number {
    const cached = depths.get(id)
    if (cached != null) return cached
    if (visiting.has(id)) return 0
    visiting.add(id)
    const children = byID.get(id)?.source_spec.group_ids ?? []
    let value = 0
    for (const child of children) {
      if (byID.has(child)) value = Math.max(value, depth(child) + 1)
    }
    visiting.delete(id)
    depths.set(id, value)
    return value
  }
  groups.forEach((group) => depth(group.id))
  return depths
}

function sourceSummary(group: ProxyGroup, subscriptionNames: Map<string, string>): string[] {
  const spec = group.source_spec
  const lines: string[] = []
  if (spec.subscription_ids?.length) {
    const names = spec.subscription_ids.map((id) => subscriptionNames.get(id) ?? "已删除订阅")
    lines.push(`订阅：${names.join("、")}`)
  }
  if (spec.node_ids.length) lines.push(`固定节点 ×${spec.node_ids.length}`)
  if (spec.regions?.length) lines.push(`地区：${spec.regions.join("/").toUpperCase()}`)
  if (spec.name_keywords?.length) lines.push(`关键词：${spec.name_keywords.join("、")}`)
  if (spec.max_latency_ms) lines.push(`延迟 ≤ ${spec.max_latency_ms} ms`)
  if (spec.limit) lines.push(`Top ${spec.limit}（${spec.sort_by === "name" ? "按名称" : "按延迟"}）`)
  if (spec.include_direct) lines.push("含 DIRECT 直连")
  return lines
}

export function BlueprintPage({ onNotice }: BlueprintPageProps) {
  const [listeners, setListeners] = useState<ListenerRecord[]>([])
  const [groups, setGroups] = useState<ProxyGroup[]>([])
  const [subscriptionNames, setSubscriptionNames] = useState<Map<string, string>>(new Map())
  const [loading, setLoading] = useState(true)
  const [nodes, setNodes] = useState<BlueprintNode[]>([])
  const [edges, setEdges] = useState<Edge[]>([])
  const [selection, setSelection] = useState<Selection>(null)
  const layoutRef = useRef(loadLayout())

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [listenerResult, groupResult, subscriptionResult] = await Promise.all([
        api.listListeners(),
        api.listProxyGroups(),
        api.listSubscriptions(),
      ])
      setListeners(listenerResult.items)
      setGroups(groupResult.items)
      setSubscriptionNames(new Map(subscriptionResult.items.map((item) => [item.id, item.name])))
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "加载蓝图数据失败", "error")
    } finally {
      setLoading(false)
    }
  }, [onNotice])

  useEffect(() => { void load() }, [load])

  // Rebuild the graph whenever server state changes; user-dragged positions
  // win over the computed layered layout.
  useEffect(() => {
    const depths = groupDepths(groups)
    const maxDepth = Math.max(0, ...depths.values())
    const layout = layoutRef.current
    const built: BlueprintNode[] = []
    const builtEdges: Edge[] = []

    listeners.forEach((listener, index) => {
      const position = layout[listener.id] ?? { x: 20, y: 40 + index * ROW_HEIGHT }
      built.push({
        id: listener.id,
        type: "listener",
        position,
        data: {
          listener,
          groupName: groups.find((group) => group.id === listener.proxy_group_id)?.name,
        },
      })
      if (groups.some((group) => group.id === listener.proxy_group_id)) {
        builtEdges.push({
          id: `L:${listener.id}`,
          source: listener.id,
          target: listener.proxy_group_id,
          animated: listener.enabled,
          style: { stroke: "#0969da", strokeWidth: 2 },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#0969da" },
        })
      }
    })

    const rowsPerColumn = new Map<number, number>()
    groups.forEach((group) => {
      const column = maxDepth - (depths.get(group.id) ?? 0)
      const row = rowsPerColumn.get(column) ?? 0
      rowsPerColumn.set(column, row + 1)
      const position = layout[group.id] ?? {
        x: 20 + COLUMN_WIDTH * (column + 1),
        y: 40 + row * ROW_HEIGHT,
      }
      const spec = group.source_spec
      const memberParts: string[] = []
      if (spec.group_ids?.length) memberParts.push(`子组 ×${spec.group_ids.length}`)
      if (spec.subscription_ids?.length) memberParts.push(`订阅 ×${spec.subscription_ids.length}`)
      if (spec.node_ids.length) memberParts.push(`固定节点 ×${spec.node_ids.length}`)
      if (spec.include_direct) memberParts.push("DIRECT")
      built.push({
        id: group.id,
        type: "group",
        position,
        data: {
          group,
          memberSummary: memberParts.length ? memberParts.join(" · ") : "空成员（按兜底行为处理）",
          resolvedCount: null,
        },
      })
      spec.group_ids?.forEach((childID, order) => {
        if (!groups.some((candidate) => candidate.id === childID)) return
        builtEdges.push({
          id: `G:${group.id}:${childID}`,
          source: group.id,
          target: childID,
          label: group.strategy === "fallback" ? `${order + 1}` : undefined,
          style: { stroke: "#1a7f37", strokeWidth: 2 },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#1a7f37" },
        })
      })
      const summary = sourceSummary(group, subscriptionNames)
      if (summary.length) {
        const sourceID = `source-${group.id}`
        const sourcePosition = layout[sourceID] ?? {
          x: 20 + COLUMN_WIDTH * (maxDepth + 2),
          y: position.y + 20,
        }
        built.push({ id: sourceID, type: "source", position: sourcePosition, data: { group, summary } })
        builtEdges.push({
          id: `S:${group.id}`,
          source: group.id,
          target: sourceID,
          style: { stroke: "#8250df", strokeDasharray: "4 3" },
        })
      }
    })

    setNodes(built)
    setEdges(builtEdges)
  }, [groups, listeners, subscriptionNames])

  const onNodesChange = useCallback((changes: NodeChange<BlueprintNode>[]) => {
    setNodes((current) => applyNodeChanges(changes, current))
    for (const change of changes) {
      if (change.type === "position" && change.position && change.dragging === false) {
        layoutRef.current[change.id] = change.position
        saveLayout(layoutRef.current)
      }
    }
  }, [])

  const onEdgesChange = useCallback((changes: EdgeChange[]) => {
    // Removals are handled in onEdgesDelete (async API calls); apply the rest.
    setEdges((current) => applyEdgeChanges(changes.filter((change) => change.type !== "remove"), current))
  }, [])

  const onConnect = useCallback(async (connection: Connection) => {
    const sourceListener = listeners.find((item) => item.id === connection.source)
    const sourceGroup = groups.find((item) => item.id === connection.source)
    const targetGroup = groups.find((item) => item.id === connection.target)
    try {
      if (sourceListener && targetGroup) {
        await api.updateListener(sourceListener.id, {
          version: sourceListener.version,
          name: sourceListener.name,
          kind: sourceListener.kind,
          bind_address: sourceListener.bind_address,
          port: sourceListener.port,
          proxy_group_id: targetGroup.id,
          enabled: sourceListener.enabled,
        })
        onNotice(`入口「${sourceListener.name}」已指向「${targetGroup.name}」`)
        await load()
        return
      }
      if (sourceGroup && targetGroup) {
        if (sourceGroup.id === targetGroup.id) return
        const existing = sourceGroup.source_spec.group_ids ?? []
        if (existing.includes(targetGroup.id)) return
        await api.updateProxyGroup(sourceGroup.id, {
          version: sourceGroup.version,
          name: sourceGroup.name,
          strategy: sourceGroup.strategy,
          source_spec: { ...sourceGroup.source_spec, group_ids: [...existing, targetGroup.id] },
          enabled: sourceGroup.enabled,
          empty_behavior: sourceGroup.empty_behavior,
          fallback_target_id: sourceGroup.fallback_target_id,
        })
        onNotice(`「${targetGroup.name}」已成为「${sourceGroup.name}」的子组`)
        await load()
        return
      }
      onNotice("只支持 入口 → 代理组 或 代理组 → 代理组 的连线", "error")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "连线失败", "error")
      await load()
    }
  }, [groups, listeners, load, onNotice])

  const onEdgesDelete = useCallback(async (deleted: Edge[]) => {
    for (const edge of deleted) {
      if (edge.id.startsWith("G:")) {
        const [, parentID, childID] = edge.id.split(":")
        const parent = groups.find((item) => item.id === parentID)
        if (!parent) continue
        try {
          await api.updateProxyGroup(parent.id, {
            version: parent.version,
            name: parent.name,
            strategy: parent.strategy,
            source_spec: {
              ...parent.source_spec,
              group_ids: (parent.source_spec.group_ids ?? []).filter((id) => id !== childID),
            },
            enabled: parent.enabled,
            empty_behavior: parent.empty_behavior,
            fallback_target_id: parent.fallback_target_id,
          })
          onNotice("已断开组间引用")
        } catch (error) {
          onNotice(error instanceof Error ? error.message : "断开引用失败", "error")
        }
      } else if (edge.id.startsWith("L:")) {
        onNotice("入口必须绑定一个代理组；如需更换，直接拖一条新连线到目标组", "error")
      }
    }
    await load()
  }, [groups, load, onNotice])

  const selectedListener = useMemo(
    () => (selection?.kind === "listener" ? listeners.find((item) => item.id === selection.id) ?? null : null),
    [listeners, selection],
  )
  const selectedGroup = useMemo(
    () => (selection?.kind === "group" ? groups.find((item) => item.id === selection.id) ?? null : null),
    [groups, selection],
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h1 className="text-xl font-semibold">蓝图编排</h1>
          <p className="mt-1 max-w-3xl text-sm leading-5 text-muted-foreground">
            根节点是入口（端口 / 协议 / 认证，并提供订阅链接）；连向代理组决定出口策略；组还可以引用子组：
            「故障切换」= 串联责任链，「自动测速」等 = 并联取最优。
          </p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          <RefreshCw className={loading ? "animate-spin" : ""} />刷新
        </Button>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_320px]">
        <section className="relative h-[640px] overflow-hidden rounded-lg border bg-white">
          {loading && nodes.length === 0 ? (
            <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
              <LoaderCircle className="size-4 animate-spin" />正在加载蓝图
            </div>
          ) : nodes.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center px-6 text-center">
              <Workflow className="mb-3 size-8 text-[#8c959f]" />
              <div className="font-medium">画布还是空的</div>
              <p className="mt-1 max-w-md text-xs leading-5 text-muted-foreground">
                先在右侧「画布工具」创建代理组和入口，然后用连线组合它们。
              </p>
            </div>
          ) : (
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={blueprintNodeTypes}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onConnect={(connection) => void onConnect(connection)}
              onEdgesDelete={(deleted) => void onEdgesDelete(deleted)}
              onNodeClick={(_, node) => {
                if (node.type === "listener") setSelection({ kind: "listener", id: node.id })
                else if (node.type === "group") setSelection({ kind: "group", id: node.id })
                else setSelection({ kind: "group", id: (node.data as { group: ProxyGroup }).group.id })
              }}
              onPaneClick={() => setSelection(null)}
              fitView
              minZoom={0.3}
              maxZoom={1.5}
              proOptions={{ hideAttribution: true }}
            >
              <Background variant={BackgroundVariant.Dots} gap={18} size={1.2} color="#d0d7de" />
              <Controls showInteractive={false} />
            </ReactFlow>
          )}
        </section>

        <aside className="h-fit rounded-lg border bg-white p-3">
          {selectedListener ? (
            <ListenerInspector
              listener={selectedListener}
              groups={groups}
              onChanged={load}
              onNotice={onNotice}
              onClose={() => setSelection(null)}
            />
          ) : selectedGroup ? (
            <GroupInspector
              group={selectedGroup}
              groups={groups}
              onChanged={load}
              onNotice={onNotice}
              onClose={() => setSelection(null)}
            />
          ) : (
            <CanvasToolbox groups={groups} onChanged={load} onNotice={onNotice} />
          )}
        </aside>
      </div>
    </div>
  )
}
