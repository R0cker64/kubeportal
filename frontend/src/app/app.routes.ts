import { Routes } from '@angular/router';
import { ServiceDetailPage } from './services/service-detail';
import { ServiceList } from './services/service-list';

export const routes: Routes = [
  { path: '', component: ServiceList, title: 'Servicios · KubePortal' },
  { path: 'services/:environment/:kind/:name', component: ServiceDetailPage, title: 'Servicio · KubePortal' },
  { path: '**', redirectTo: '' },
];
