import { Routes } from '@angular/router';
import { adminGuard } from './admin/admin.guard';
import { AdminNodes } from './admin/nodes';
import { AdminTeams } from './admin/teams';
import { AdminUnassigned } from './admin/unassigned';
import { ServiceDetailPage } from './services/service-detail';
import { ServiceList } from './services/service-list';

export const routes: Routes = [
  { path: '', component: ServiceList, title: 'Servicios · KubePortal' },
  { path: 'services/:environment/:kind/:name', component: ServiceDetailPage, title: 'Servicio · KubePortal' },
  {
    path: 'admin',
    canActivate: [adminGuard],
    children: [
      { path: 'nodes', component: AdminNodes, title: 'Nodos · KubePortal' },
      { path: 'teams', component: AdminTeams, title: 'Equipos · KubePortal' },
      { path: 'unassigned', component: AdminUnassigned, title: 'Sin equipo · KubePortal' },
      { path: '', redirectTo: 'nodes', pathMatch: 'full' },
    ],
  },
  { path: '**', redirectTo: '' },
];
