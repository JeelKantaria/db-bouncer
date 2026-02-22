import os

xml = f"""<mxfile host="app.diagrams.net" modified="2026-02-22T00:00:00.000Z" agent="antigravity" version="21.0.0" type="device">
  <diagram id="diagram_1" name="Architecture">
    <mxGraphModel dx="1200" dy="800" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="1169" pageHeight="827" background="#ffffff" math="0" shadow="0">
      <root>
        <mxCell id="0" />
        <mxCell id="1" parent="0" />
        
        <!-- App Pods -->
        <mxCell id="app1" value="App Pod 1" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1">
          <mxGeometry x="40" y="200" width="100" height="40" as="geometry" />
        </mxCell>
        <mxCell id="app2" value="App Pod 2" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1">
          <mxGeometry x="40" y="260" width="100" height="40" as="geometry" />
        </mxCell>
        <mxCell id="appN" value="App Pod N" style="rounded=1;whiteSpace=wrap;html=1;dashed=1;" vertex="1" parent="1">
          <mxGeometry x="40" y="320" width="100" height="40" as="geometry" />
        </mxCell>
        <mxCell id="admin" value="Admin" style="shape=actor;whiteSpace=wrap;html=1;" vertex="1" parent="1">
          <mxGeometry x="60" y="450" width="40" height="50" as="geometry" />
        </mxCell>

        <!-- DBBouncer Container -->
        <mxCell id="bouncer" value="DBBouncer" style="swimlane;whiteSpace=wrap;html=1;startSize=30;" vertex="1" parent="1">
          <mxGeometry x="240" y="100" width="540" height="440" as="geometry" />
        </mxCell>
        
        <!-- DBBouncer Internals -->
        <mxCell id="proxy_pg" value="Proxy Server&#10;(PostgreSQL :6432)" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="bouncer">
          <mxGeometry x="40" y="80" width="120" height="60" as="geometry" />
        </mxCell>
        <mxCell id="proxy_my" value="Proxy Server&#10;(MySQL :3307)" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="bouncer">
          <mxGeometry x="40" y="160" width="120" height="60" as="geometry" />
        </mxCell>
        
        <mxCell id="api_server" value="REST API&#10;(:8080)" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#fff2cc;strokeColor=#d6b656;" vertex="1" parent="bouncer">
          <mxGeometry x="40" y="280" width="120" height="60" as="geometry" />
        </mxCell>
        
        <mxCell id="router" value="Router" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#d5e8d4;strokeColor=#82b366;" vertex="1" parent="bouncer">
          <mxGeometry x="220" y="120" width="90" height="60" as="geometry" />
        </mxCell>
        
        <mxCell id="pool" value="Pool Manager" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#e1d5e7;strokeColor=#9673a6;" vertex="1" parent="bouncer">
          <mxGeometry x="380" y="120" width="120" height="80" as="geometry" />
        </mxCell>

        <mxCell id="health" value="Health Checker" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#f8cecc;strokeColor=#b85450;" vertex="1" parent="bouncer">
          <mxGeometry x="220" y="280" width="100" height="60" as="geometry" />
        </mxCell>

        <mxCell id="metrics" value="Metrics (Prometheus)" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#eeeeee;strokeColor=#36393d;" vertex="1" parent="bouncer">
          <mxGeometry x="380" y="280" width="120" height="60" as="geometry" />
        </mxCell>

        <mxCell id="config" value="Config Watcher" style="shape=document;whiteSpace=wrap;html=1;boundedLbl=1;fillColor=#ffffff;" vertex="1" parent="bouncer">
          <mxGeometry x="220" y="360" width="100" height="60" as="geometry" />
        </mxCell>

        <!-- Tenant DBs -->
        <mxCell id="t1" value="Tenant 1 DB&#10;(PostgreSQL)" style="shape=cylinder3;whiteSpace=wrap;html=1;boundedLbl=1;backgroundOutline=1;size=15;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="1">
          <mxGeometry x="900" y="150" width="100" height="80" as="geometry" />
        </mxCell>
        <mxCell id="t2" value="Tenant 2 DB&#10;(MySQL)" style="shape=cylinder3;whiteSpace=wrap;html=1;boundedLbl=1;backgroundOutline=1;size=15;fillColor=#ffe6cc;strokeColor=#d79b00;" vertex="1" parent="1">
          <mxGeometry x="900" y="270" width="100" height="80" as="geometry" />
        </mxCell>
        <mxCell id="tN" value="Tenant N DB" style="shape=cylinder3;whiteSpace=wrap;html=1;boundedLbl=1;backgroundOutline=1;size=15;dashed=1;" vertex="1" parent="1">
          <mxGeometry x="900" y="390" width="100" height="80" as="geometry" />
        </mxCell>

        <!-- Edges -->
        <mxCell id="e1" edge="1" parent="1" source="app1" target="proxy_pg" style="edgeStyle=orthogonalEdgeStyle;rounded=0;orthogonalLoop=1;jettySize=auto;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e2" edge="1" parent="1" source="app2" target="proxy_pg" style="edgeStyle=orthogonalEdgeStyle;rounded=0;orthogonalLoop=1;jettySize=auto;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e3" edge="1" parent="1" source="appN" target="proxy_my" style="edgeStyle=orthogonalEdgeStyle;rounded=0;orthogonalLoop=1;jettySize=auto;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e4" edge="1" parent="1" source="admin" target="api_server" style="edgeStyle=orthogonalEdgeStyle;rounded=0;orthogonalLoop=1;jettySize=auto;html=1;dashed=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>

        <!-- Internal Edges (using source/target mapped to parent=1 relative positions or just direct references) -->
        <mxCell id="e_px_rt1" edge="1" parent="1" source="proxy_pg" target="router" style="edgeStyle=orthogonalEdgeStyle;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e_px_rt2" edge="1" parent="1" source="proxy_my" target="router" style="edgeStyle=orthogonalEdgeStyle;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        
        <mxCell id="e_rt_pool" edge="1" parent="1" source="router" target="pool" style="edgeStyle=orthogonalEdgeStyle;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        
        <mxCell id="e_pool_t1" edge="1" parent="1" source="pool" target="t1" style="edgeStyle=orthogonalEdgeStyle;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e_pool_t2" edge="1" parent="1" source="pool" target="t2" style="edgeStyle=orthogonalEdgeStyle;html=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e_pool_tN" edge="1" parent="1" source="pool" target="tN" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>

        <!-- Management Edges -->
        <mxCell id="e_api_rt" edge="1" parent="1" source="api_server" target="router" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;startArrow=classic;endArrow=classic;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e_api_pool" edge="1" parent="1" source="api_server" target="pool" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;startArrow=classic;endArrow=classic;">
          <mxGeometry relative="1" as="geometry">
             <Array as="points">
              <mxPoint x="490" y="310" />
            </Array>
          </mxGeometry>
        </mxCell>
        <mxCell id="e_hlth_pool" edge="1" parent="1" source="health" target="pool" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;startArrow=classic;endArrow=none;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        
        <mxCell id="e_hlth_t1" edge="1" parent="1" source="health" target="t1" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;strokeColor=#b85450;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e_hlth_t2" edge="1" parent="1" source="health" target="t2" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;strokeColor=#b85450;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>

        <mxCell id="e_pool_met" edge="1" parent="1" source="pool" target="metrics" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        
        <mxCell id="e_cfg_rt" edge="1" parent="1" source="config" target="router" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
        <mxCell id="e_cfg_pool" edge="1" parent="1" source="config" target="pool" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;">
          <mxGeometry relative="1" as="geometry" />
        </mxCell>
      </root>
    </mxGraphModel>
  </diagram>
</mxfile>
"""

os.makedirs("/Users/jeelk/Documents/GitHub/JeelKantaria/db-bouncer/docs", exist_ok=True)
with open("/Users/jeelk/Documents/GitHub/JeelKantaria/db-bouncer/docs/architecture.drawio", "w") as f:
    f.write(xml)

print("Created /Users/jeelk/Documents/GitHub/JeelKantaria/db-bouncer/docs/architecture.drawio")
